package toolexec

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GiGurra/rewire/internal/mockgen"
	"github.com/GiGurra/rewire/internal/rewriter"
)

// Run executes the toolexec wrapper. It is called when rewire is invoked as:
//
//	rewire /path/to/go/tool/compile <args...>
//
// For compile invocations, it rewrites functions that are targets of
// rewire.Func calls in test files. Registration files are generated
// for test compilations to connect mock variables to the rewire registry.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "rewire toolexec: missing tool argument")
		return 1
	}

	tool := args[0]
	toolArgs := args[1:]

	if !isCompileTool(tool) {
		return execTool(tool, toolArgs)
	}

	pkgPath := findFlag(toolArgs, "-p")
	if pkgPath == "" {
		return execTool(tool, toolArgs)
	}

	_, moduleRoot := findModuleInfo()
	if moduleRoot == "" {
		return execTool(tool, toolArgs)
	}

	// Load the set of functions to mock (scanned from test files)
	targets, instantiations, byInstance, _ := loadOrScanMockTargets(moduleRoot)

	funcsToMock := targets[pkgPath]
	pkgByInstance := byInstance[pkgPath]
	isTest := hasTestFiles(toolArgs)

	// Reject intrinsic functions early
	for _, fn := range funcsToMock {
		if isIntrinsic(pkgPath, fn) {
			fmt.Fprintf(os.Stderr,
				"rewire: error: function %s.%s cannot be mocked.\n"+
					"  It is a compiler intrinsic — the Go compiler replaces calls to it\n"+
					"  with a CPU instruction, bypassing any mock wrapper.\n"+
					"  See: $GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go\n",
				pkgPath, fn)
			return 1
		}
	}

	if len(funcsToMock) == 0 && !isTest {
		return execTool(tool, toolArgs)
	}

	rewrittenArgs, cleanup, err := rewriteCompileArgs(toolArgs, pkgPath, funcsToMock, pkgByInstance, isTest, targets, instantiations, byInstance)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rewire: rewrite failed for %s: %v\n", pkgPath, err)
		return 1
	}
	if cleanup != nil {
		defer cleanup()
	}

	return execTool(tool, rewrittenArgs)
}

func isCompileTool(tool string) bool {
	return filepath.Base(tool) == "compile"
}

func hasTestFiles(args []string) bool {
	for _, arg := range args {
		if strings.HasSuffix(arg, "_test.go") {
			return true
		}
	}
	return false
}

// findModuleInfo returns the module path and root directory.
func findModuleInfo() (modulePath string, moduleRoot string) {
	dir, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			return parseModulePath(string(data)), dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func parseModulePath(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

func findFlag(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// rewriteCompileArgs rewrites only the specific functions listed in funcsToMock.
// For test compilations, it generates a registration file directly from targets.
func rewriteCompileArgs(args []string, pkgPath string, funcsToMock []string, pkgByInstance map[string]bool, isTest bool, allTargets mockTargets, allInstantiations genericInstantiations, allByInstance byInstanceTargets) ([]string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "rewire-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	newArgs := make([]string, len(args))
	copy(newArgs, args)

	// Rewrite targeted functions in non-test source files
	if len(funcsToMock) > 0 {
		var rewrittenFuncs []string

		for i, arg := range newArgs {
			if !strings.HasSuffix(arg, ".go") || strings.HasSuffix(arg, "_test.go") {
				continue
			}

			src, err := os.ReadFile(arg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("reading %s: %w", arg, err)
			}

			rewritten := src
			for _, fn := range funcsToMock {
				opts := rewriter.RewriteOptions{ByInstance: pkgByInstance[fn]}
				result, err := rewriter.RewriteSourceOpts(rewritten, fn, opts)
				if err != nil {
					if strings.Contains(err.Error(), "not found") {
						continue
					}
					cleanup()
					return nil, nil, fmt.Errorf("rewriting %s in %s: %w", fn, arg, err)
				}
				rewritten = result
				rewrittenFuncs = append(rewrittenFuncs, fn)
			}

			if string(rewritten) == string(src) {
				continue
			}

			tmpFile := filepath.Join(tmpDir, filepath.Base(arg))
			if err := os.WriteFile(tmpFile, rewritten, 0644); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("writing temp file: %w", err)
			}
			newArgs[i] = tmpFile
		}

		// Verify all requested functions were found
		rewrittenSet := map[string]bool{}
		for _, fn := range rewrittenFuncs {
			rewrittenSet[fn] = true
		}
		for _, fn := range funcsToMock {
			if !rewrittenSet[fn] {
				cleanup()
				return nil, nil, fmt.Errorf(
					"function %s.%s cannot be mocked — not found in any source file.\n"+
						"  The function may be implemented in assembly or excluded by build constraints",
					pkgPath, fn)
			}
		}

		// If any rewritten function is generic, the rewriter added imports
		// for reflect and sync. If any rewritten function was rewritten with
		// ByInstance=true (non-generic method case), the rewriter also added
		// an import for sync. Both cases need those packages reachable via
		// -importcfg even if the original source didn't import them.
		needsReflect := false
		needsSync := false
		for _, fn := range funcsToMock {
			if isGenericFunc(pkgPath, fn) {
				needsReflect = true
				needsSync = true
			}
			if pkgByInstance[fn] {
				needsSync = true
			}
		}
		if needsReflect || needsSync {
			stdPkgs := []string{}
			if needsReflect {
				stdPkgs = append(stdPkgs, "reflect")
			}
			if needsSync {
				stdPkgs = append(stdPkgs, "sync")
			}
			patched, extraCleanup, err := ensureStdImportsInCfg(newArgs, stdPkgs...)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("patching importcfg: %w", err)
			}
			newArgs = patched
			if extraCleanup != nil {
				prevCleanup := cleanup
				cleanup = func() {
					extraCleanup()
					prevCleanup()
				}
			}
		}
	}

	// For test compilations, generate interface-mock backing structs
	// and then the per-target registration file. Mock structs are
	// emitted first so the registration file can refer to any symbols
	// they expose (not currently needed, but keeps things clean).
	if isTest {
		mockFiles, mockCleanup, err := generateInterfaceMocks(args, tmpDir)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("generating interface mocks: %w", err)
		}
		if mockCleanup != nil {
			prevCleanup := cleanup
			cleanup = func() {
				mockCleanup()
				prevCleanup()
			}
		}
		if len(mockFiles) > 0 {
			newArgs = append(newArgs, mockFiles...)
			// Interface mocks pull in "sync" (for the per-instance
			// dispatch tables) and may reference packages the original
			// test source didn't import. Patch the importcfg.
			patched, extraCleanup, err := ensureStdImportsInCfg(newArgs, "sync")
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("patching importcfg for interface mocks: %w", err)
			}
			newArgs = patched
			if extraCleanup != nil {
				prevCleanup := cleanup
				cleanup = func() {
					extraCleanup()
					prevCleanup()
				}
			}
		}

		regFile, err := generateRegistration(args, allTargets, allInstantiations, allByInstance)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("generating registration: %w", err)
		}
		if regFile != "" {
			tmpReg := filepath.Join(tmpDir, "_rewire_register_test.go")
			if err := os.WriteFile(tmpReg, []byte(regFile), 0644); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("writing registration file: %w", err)
			}
			newArgs = append(newArgs, tmpReg)
		}
	}

	return newArgs, cleanup, nil
}

// generateInterfaceMocks finds every rewire.NewMock[I] reference in the
// test package's source files (scoped to this compilation, not
// module-wide) and emits a backing struct file for each one into
// tmpDir. Returns the paths of the generated files so the caller can
// append them to the compiler args.
func generateInterfaceMocks(compileArgs []string, tmpDir string) ([]string, func(), error) {
	// Walk the test files in this compile and collect mocked interface refs.
	pkgMockedIfaces := mockedInterfaces{}
	pkgName := ""
	for _, arg := range compileArgs {
		if !strings.HasSuffix(arg, ".go") {
			continue
		}
		_, _, _, fileMocks := scanFileForMockCalls(arg)
		for ip, ifaces := range fileMocks {
			if pkgMockedIfaces[ip] == nil {
				pkgMockedIfaces[ip] = map[string]bool{}
			}
			for name := range ifaces {
				pkgMockedIfaces[ip][name] = true
			}
		}
		// Need the test package name for the emitted file's package clause.
		if pkgName == "" {
			fset := token.NewFileSet()
			if f, err := parser.ParseFile(fset, arg, nil, parser.PackageClauseOnly); err == nil {
				pkgName = f.Name.Name
			}
		}
	}
	if len(pkgMockedIfaces) == 0 {
		return nil, nil, nil
	}
	if pkgName == "" {
		return nil, nil, fmt.Errorf("could not determine test package name for interface mock generation")
	}

	var generatedPaths []string
	for importPath, ifaces := range pkgMockedIfaces {
		// Locate the source file in the interface's declaring package
		// that contains each interface declaration. A single package
		// can split interfaces across files, so we try each .go file
		// until we find one that defines the interface.
		pkgDir, err := resolvePackageDir(importPath)
		if err != nil {
			return nil, nil, fmt.Errorf("locating package %s for interface mock generation: %w", importPath, err)
		}

		for ifaceName := range ifaces {
			srcBytes, err := readInterfaceSource(pkgDir, ifaceName)
			if err != nil {
				return nil, nil, fmt.Errorf("reading source of interface %s.%s: %w", importPath, ifaceName, err)
			}

			alias := defaultPkgAlias(importPath)
			generated, err := mockgen.GenerateRewireMock(srcBytes, ifaceName, importPath, alias, pkgName)
			if err != nil {
				return nil, nil, fmt.Errorf("generating mock for %s.%s: %w", importPath, ifaceName, err)
			}
			outPath := filepath.Join(tmpDir, fmt.Sprintf("_rewire_mock_%s_%s_test.go", alias, ifaceName))
			if err := os.WriteFile(outPath, generated, 0644); err != nil {
				return nil, nil, fmt.Errorf("writing generated mock file: %w", err)
			}
			generatedPaths = append(generatedPaths, outPath)
		}
	}
	return generatedPaths, nil, nil
}

// resolvePackageDir resolves an import path to an absolute directory
// containing its source files, via go/build.
func resolvePackageDir(importPath string) (string, error) {
	pkg, err := build.Default.Import(importPath, ".", build.FindOnly)
	if err != nil {
		return "", err
	}
	if pkg.Dir == "" {
		return "", fmt.Errorf("package %s has no resolved directory", importPath)
	}
	return pkg.Dir, nil
}

// readInterfaceSource finds the .go file in pkgDir that declares
// ifaceName as an interface type and returns its raw bytes. Test files
// and generated files are excluded. Returns an error if no file
// declares the interface.
func readInterfaceSource(pkgDir, ifaceName string) ([]byte, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fullPath := filepath.Join(pkgDir, name)
		f, err := parser.ParseFile(fset, fullPath, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != ifaceName {
					continue
				}
				if _, ok := ts.Type.(*ast.InterfaceType); !ok {
					return nil, fmt.Errorf("%s in package %s is not an interface", ifaceName, pkgDir)
				}
				return os.ReadFile(fullPath)
			}
		}
	}
	return nil, fmt.Errorf("interface %s not found in package directory %s", ifaceName, pkgDir)
}

// defaultPkgAlias returns the default Go local name for an import path
// (its last path segment).
func defaultPkgAlias(path string) string {
	segments := strings.Split(path, "/")
	return segments[len(segments)-1]
}

// generateRegistration creates init() code that registers mock var pointers.
// It works directly from mock targets — no manifests needed.
func generateRegistration(compileArgs []string, targets mockTargets, instantiations genericInstantiations, byInstance byInstanceTargets) (string, error) {
	pkgName := ""
	allImports := map[string]bool{}

	fset := token.NewFileSet()
	for _, arg := range compileArgs {
		if !strings.HasSuffix(arg, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, arg, nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		if pkgName == "" {
			pkgName = f.Name.Name
		}
		for _, imp := range f.Imports {
			allImports[strings.Trim(imp.Path.Value, `"`)] = true
		}
	}

	if pkgName == "" {
		return "", nil
	}

	// If the test package doesn't import rewire, we can't emit a
	// registration file that calls rewire.Register. This also avoids an
	// import cycle when compiling the rewire package's own tests.
	if !allImports["github.com/GiGurra/rewire/pkg/rewire"] {
		return "", nil
	}

	type entry struct {
		importPath string
		alias      string
		funcNames  []string
	}
	var entries []entry
	usedAliases := map[string]int{}

	for importPath, funcs := range targets {
		if !allImports[importPath] || len(funcs) == 0 {
			continue
		}
		if importPath == "github.com/GiGurra/rewire/pkg/rewire" {
			continue
		}

		// Filter out intrinsics
		var mockable []string
		for _, fn := range funcs {
			if !isIntrinsic(importPath, fn) {
				mockable = append(mockable, fn)
			}
		}
		if len(mockable) == 0 {
			continue
		}

		// Derive package qualifier from import path (last segment)
		segments := strings.Split(importPath, "/")
		pkgLocalName := segments[len(segments)-1]
		alias := "_rewire_" + pkgLocalName
		if count := usedAliases[alias]; count > 0 {
			alias = fmt.Sprintf("_rewire_%s_%d", pkgLocalName, count+1)
		}
		usedAliases[alias]++

		entries = append(entries, entry{
			importPath: importPath,
			alias:      alias,
			funcNames:  mockable,
		})
	}

	if len(entries) == 0 {
		return "", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	b.WriteString("import (\n")
	b.WriteString("\t\"github.com/GiGurra/rewire/pkg/rewire\"\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\t%s %q\n", e.alias, e.importPath)
	}
	b.WriteString(")\n\n")
	b.WriteString("func init() {\n")
	for _, e := range entries {
		for _, fn := range e.funcNames {
			fmt.Fprintf(&b, "\trewire.Register(%q, &%s.%s)\n",
				e.importPath+"."+fn, e.alias, mockVarName(fn))

			if isGenericFunc(e.importPath, fn) {
				// Generic: emit one RegisterReal call per unique
				// instantiation the scanner found, passing a
				// concrete function value like `pkg.Real_Map[int,
				// string]`. rewire.RegisterReal uses reflect.TypeOf
				// to derive a unique lookup key per type signature.
				combos := instantiations[e.importPath][fn]
				for _, typeArgs := range combos {
					fmt.Fprintf(&b, "\trewire.RegisterReal(%q, %s.%s[%s])\n",
						e.importPath+"."+fn, e.alias, realVarName(fn), strings.Join(typeArgs, ", "))
				}
			} else {
				fmt.Fprintf(&b, "\trewire.RegisterReal(%q, %s.%s)\n",
					e.importPath+"."+fn, e.alias, realVarName(fn))
			}

			// Methods referenced by rewire.InstanceMethod /
			// RestoreInstanceMethod need the per-instance table registered.
			// The rewriter emitted a Mock_Type_Method_ByInstance sync.Map
			// at the same package level.
			if byInstance[e.importPath][fn] {
				fmt.Fprintf(&b, "\trewire.RegisterByInstance(%q, &%s.%s_ByInstance)\n",
					e.importPath+"."+fn, e.alias, mockVarName(fn))
			}
		}
	}
	b.WriteString("}\n")

	return b.String(), nil
}

// ensureStdImportsInCfg patches the -importcfg arg to include the given
// stdlib packages, resolving their export .a files via `go list -export`
// when they aren't already listed. Returns the updated args and a
// cleanup function for the temp file if one was created.
//
// This is necessary because the Go compiler's -importcfg only lists
// packages the original source imports. When rewire's rewriter adds
// imports for reflect/sync, those packages aren't visible to the
// compiler unless we extend the importcfg.
func ensureStdImportsInCfg(args []string, pkgs ...string) ([]string, func(), error) {
	cfgIdx := -1
	var cfgPath string
	for i, arg := range args {
		if arg == "-importcfg" && i+1 < len(args) {
			cfgIdx = i + 1
			cfgPath = args[i+1]
			break
		}
	}
	if cfgIdx < 0 {
		return args, nil, nil
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading importcfg %s: %w", cfgPath, err)
	}

	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "packagefile ")
		if !ok {
			continue
		}
		if eq := strings.Index(rest, "="); eq > 0 {
			existing[strings.TrimSpace(rest[:eq])] = true
		}
	}

	var missing []string
	for _, p := range pkgs {
		if !existing[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return args, nil, nil
	}

	exports, err := resolveStdExportPaths(missing)
	if err != nil {
		return nil, nil, err
	}

	var buf bytes.Buffer
	buf.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		buf.WriteByte('\n')
	}
	for _, p := range missing {
		path, ok := exports[p]
		if !ok || path == "" {
			return nil, nil, fmt.Errorf("could not resolve export path for %s", p)
		}
		fmt.Fprintf(&buf, "packagefile %s=%s\n", p, path)
	}

	tmp, err := os.CreateTemp("", "rewire-importcfg-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating patched importcfg: %w", err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, nil, fmt.Errorf("writing patched importcfg: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, nil, fmt.Errorf("closing patched importcfg: %w", err)
	}

	newArgs := make([]string, len(args))
	copy(newArgs, args)
	newArgs[cfgIdx] = tmp.Name()

	cleanup := func() { _ = os.Remove(tmp.Name()) }
	return newArgs, cleanup, nil
}

// resolveStdExportPaths runs `go list -export` to find the compiled .a
// files for each package, with GOFLAGS stripped so the recursive
// toolexec doesn't fire.
func resolveStdExportPaths(pkgs []string) (map[string]string, error) {
	listArgs := append([]string{"list", "-export", "-f", "{{.ImportPath}}|{{.Export}}"}, pkgs...)
	cmd := exec.Command("go", listArgs...)

	// Strip GOFLAGS to avoid `go list` re-invoking rewire via toolexec.
	env := os.Environ()
	filtered := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "GOFLAGS=") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list -export failed: %w\nstderr: %s", err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("go list -export failed: %w", err)
	}
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sep := strings.Index(line, "|")
		if sep < 0 {
			continue
		}
		result[line[:sep]] = line[sep+1:]
	}
	return result, nil
}

// isGenericFunc reports whether the named target in importPath is a
// plain generic function OR a method on a generic type. It resolves the
// package directory via go/build and AST-parses the package's non-test
// Go files.
//
// For "Func", the function's own TypeParams must be non-empty.
// For "(*Type).Method" or "Type.Method", the receiver type's TypeSpec
// must have TypeParams. (Go 1.18+ forbids method-level type params, so
// all the parameters come from the type declaration.)
//
// Any parsing failure is treated as "not generic" — a false negative
// just causes codegen to emit a RegisterReal call that fails to compile
// with a clear error, so the user sees something fix-able.
func isGenericFunc(importPath, funcName string) bool {
	typeName, _, isMethod := parseTargetName(funcName)
	if isMethod && typeName == "" {
		return false
	}
	pkg, err := build.Default.Import(importPath, ".", build.FindOnly)
	if err != nil || pkg.Dir == "" {
		return false
	}
	entries, err := os.ReadDir(pkg.Dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			if isMethod {
				// Looking for a type spec with matching name and type params.
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name != typeName {
						continue
					}
					return ts.TypeParams != nil && ts.TypeParams.NumFields() > 0
				}
				continue
			}
			// Plain function lookup.
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != funcName {
				continue
			}
			return fd.Type.TypeParams != nil && fd.Type.TypeParams.NumFields() > 0
		}
	}
	return false
}

// parseTargetName decomposes a rewire target name into its shape:
//
//	"Func"              → ("",         "Func",   false)
//	"Type.Method"       → ("Type",     "Method", true)
//	"(*Type).Method"    → ("Type",     "Method", true)
//
// Mirrors the logic the rewriter uses so the codegen can reason about
// target kinds without touching the rewriter package.
func parseTargetName(name string) (typeName, methodName string, isMethod bool) {
	if strings.HasPrefix(name, "(*") {
		if idx := strings.Index(name, ")."); idx > 2 {
			return name[2:idx], name[idx+2:], true
		}
		return "", "", false
	}
	if idx := strings.LastIndex(name, "."); idx > 0 {
		return name[:idx], name[idx+1:], true
	}
	return "", name, false
}

// mockVarName converts a target name to the corresponding Mock_ variable name.
// "Func"              → "Mock_Func"
// "(*Server).Handle"  → "Mock_Server_Handle"
// "Point.String"      → "Mock_Point_String"
func mockVarName(targetName string) string {
	name := strings.NewReplacer("(*", "", ")", "", ".", "_").Replace(targetName)
	return "Mock_" + name
}

// realVarName converts a target name to the corresponding Real_ variable name,
// which holds the pre-rewrite implementation (a function value or method
// expression, depending on the target).
func realVarName(targetName string) string {
	name := strings.NewReplacer("(*", "", ")", "", ".", "_").Replace(targetName)
	return "Real_" + name
}

func execTool(tool string, args []string) int {
	cmd := exec.Command(tool, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "rewire: exec %s: %v\n", tool, err)
		return 1
	}
	return 0
}
