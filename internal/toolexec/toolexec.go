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
	"sync"
	"syscall"

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

	// Fast path for anything that isn't a compile invocation, or
	// that is but doesn't give us enough context to act on. All of
	// these paths have ZERO cleanup work, so we can use syscall.Exec
	// to replace the rewire process with the target tool directly —
	// saving ~10 ms of fork+wait overhead per invocation.
	if !isCompileTool(tool) {
		return execToolReplace(tool, toolArgs)
	}
	pkgPath := findFlag(toolArgs, "-p")
	if pkgPath == "" {
		return execToolReplace(tool, toolArgs)
	}
	_, moduleRoot := findModuleInfo()
	if moduleRoot == "" {
		return execToolReplace(tool, toolArgs)
	}

	if profileEnabled.Load() {
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stderr, "rewire-debug pkg=%s cwd=%s moduleRoot=%s\n", pkgPath, cwd, moduleRoot)
	}

	defer profileStage("compile-wrap", pkgPath)()

	// Load the set of functions to mock (scanned from test files)
	scanDone := profileStage("scan", "")
	targets, instantiations, byInstance, _ := loadOrScanMockTargets(moduleRoot)
	scanDone()

	funcsToMock := targets[pkgPath]
	pkgByInstance := byInstance[pkgPath]
	isTest := hasTestFiles(toolArgs)

	if profileEnabled.Load() && (pkgPath == "strings" || pkgPath == "os" || pkgPath == "math") {
		fmt.Fprintf(os.Stderr, "rewire-debug pkg=%s funcsToMock=%v isTest=%v\n", pkgPath, funcsToMock, isTest)
	}

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

	// Nothing to rewrite for this package and it's not a test
	// compilation that needs a registration file either — pass
	// through to the real compiler with no fork overhead. The
	// in-flight compile-wrap profile defer is unreachable after
	// syscall.Exec, so we end it manually here.
	if len(funcsToMock) == 0 && !isTest {
		return execToolReplace(tool, toolArgs)
	}

	// For test compilations: check if the mock target set changed
	// since the last build. When it has, the build cache contains
	// stale .a files for target packages (missing the new mock vars).
	// Rebuilding just those packages isn't enough — the linker
	// verifies fingerprints across the dependency tree — so we clear
	// the cache and ask the user to re-run.
	if isTest {
		if msg := checkAndInvalidateStaleTargets(moduleRoot, targets); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			return 1
		}
	}

	rewrittenArgs, cleanup, err := rewriteCompileArgs(toolArgs, pkgPath, funcsToMock, pkgByInstance, isTest, targets, instantiations, byInstance)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rewire: rewrite failed for %s: %v\n", pkgPath, err)
		return 1
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Rewrite path uses execTool (fork+wait) because cleanup() needs
	// to run after the compile finishes to remove temp files. If we
	// syscall.Exec'd here, the temp files would leak.
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
	defer profileStage("rewrite-compile-args", pkgPath)()
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
				opts := rewriter.RewriteOptions{
					ByInstance: pkgByInstance[fn],
					ImportPath: pkgPath,
				}
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

		// When any function was rewritten with ImportPath set, the
		// wrappers reference _rewire_getProfLabel to read the
		// current goroutine's pprof labels pointer (used as the
		// goroutine-tree identity key). The linkname to
		// runtime/pprof.runtime_getProfLabel lives in a sidecar
		// file written once per package. Each mocked function has
		// its own Mock_Foo_ByGoroutine sync.Map in the rewritten
		// file; the sidecar only hosts the linkname stub so the
		// wrappers have something to call.
		//
		// Emitting inline per-wrapper was tried first; it caused
		// duplicate declarations across multiple functions in the
		// same package, and comment attachment through the AST
		// splice was brittle. A sidecar sidesteps both issues.
		if len(rewrittenFuncs) > 0 {
			pkgName, err := readPackageNameFromArgs(newArgs)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("determining package name for linkname sidecar: %w", err)
			}
			sidecar := fmt.Sprintf(`package %s

import "unsafe"

//go:linkname _rewire_getProfLabel runtime/pprof.runtime_getProfLabel
func _rewire_getProfLabel() unsafe.Pointer
`, pkgName)
			sidecarPath := filepath.Join(tmpDir, "_rewire_linkname.go")
			if err := os.WriteFile(sidecarPath, []byte(sidecar), 0644); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("writing linkname sidecar: %w", err)
			}
			newArgs = append(newArgs, sidecarPath)
		}

		// If any rewritten function is generic, the rewriter added imports
		// for reflect and sync. If any rewritten function was rewritten with
		// ByInstance=true (non-generic method case), the rewriter also added
		// an import for sync. Both cases need those packages reachable via
		// -importcfg even if the original source didn't import them.
		needsReflect := false
		// The rewriter always adds a sync.Map for goroutine-local
		// dispatch when ImportPath is set (which it always is in
		// the toolexec compile path), so sync is needed as soon as
		// anything was rewritten.
		needsSync := len(rewrittenFuncs) > 0
		for _, fn := range funcsToMock {
			if isGenericFunc(pkgPath, fn) {
				needsReflect = true
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
//
// For generic interfaces, each unique (ifaceName, type-args)
// instantiation produces its own backing struct file. The mock for
// Container[int] is independent from Container[string] — distinct
// struct types, distinct factory keys, distinct per-instance dispatch
// tables.
func generateInterfaceMocks(compileArgs []string, tmpDir string) ([]string, func(), error) {
	defer profileStage("iface-mock-gen", "")()
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
				pkgMockedIfaces[ip] = map[string][]mockInstance{}
			}
			for name, instances := range ifaces {
				pkgMockedIfaces[ip][name] = append(pkgMockedIfaces[ip][name], instances...)
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

	// Dedupe instances per interface so two test files referencing
	// the same instantiation only generate one backing struct.
	for _, ifaces := range pkgMockedIfaces {
		for name, instances := range ifaces {
			ifaces[name] = dedupeMockInstances(instances)
		}
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

		for ifaceName, instances := range ifaces {
			srcBytes, err := readInterfaceSource(pkgDir, ifaceName)
			if err != nil {
				return nil, nil, fmt.Errorf("reading source of interface %s.%s: %w", importPath, ifaceName, err)
			}

			alias := defaultPkgAlias(importPath)
			for _, inst := range instances {
				generated, err := mockgen.GenerateRewireMock(srcBytes, ifaceName, importPath, alias, pkgName, inst.TypeArgs, inst.TypeArgImports, resolveInterfaceSource, listPackageExportedTypes)
				if err != nil {
					return nil, nil, fmt.Errorf("generating mock for %s.%s%s: %w", importPath, ifaceName, formatTypeArgs(inst.TypeArgs), err)
				}
				outPath := filepath.Join(tmpDir, fmt.Sprintf("_rewire_mock_%s_%s%s_test.go", alias, ifaceName, mangleTypeArgs(inst.TypeArgs)))
				if err := os.WriteFile(outPath, generated, 0644); err != nil {
					return nil, nil, fmt.Errorf("writing generated mock file: %w", err)
				}
				generatedPaths = append(generatedPaths, outPath)
			}
		}
	}
	return generatedPaths, nil, nil
}

// formatTypeArgs renders a type-arg combo as user-facing source text
// for error messages, e.g. ["int"] → "[int]". An empty combo (the
// non-generic case) returns "".
func formatTypeArgs(typeArgs []string) string {
	if len(typeArgs) == 0 {
		return ""
	}
	return "[" + strings.Join(typeArgs, ", ") + "]"
}

// mangleTypeArgs renders a type-arg combo as a Go-identifier-safe
// suffix for generated filenames, e.g. ["*time.Time", "int"] →
// "_ptr_time_Time_int". An empty combo returns "" so non-generic
// filenames stay unchanged.
func mangleTypeArgs(typeArgs []string) string {
	if len(typeArgs) == 0 {
		return ""
	}
	r := strings.NewReplacer(
		"*", "ptr_",
		".", "_",
		"[", "_",
		"]", "_",
		" ", "",
		",", "_",
		"/", "_",
	)
	return "_" + r.Replace(strings.Join(typeArgs, "_"))
}

// packageDirCache memoizes resolvePackageDir results for the lifetime
// of a single toolexec invocation. `go list` isn't slow, but a compile
// step that mocks several interfaces from the same package would
// otherwise shell out once per interface — this keeps it to once per
// package path.
var packageDirCache sync.Map // importPath (string) → dir (string) or error

// resolvePackageDir resolves an import path to an absolute directory
// containing its source files. Uses `go list -find -f '{{.Dir}}'` so
// resolution matches whatever the surrounding Go build system would
// use — replace directives in go.mod, workspace files (go.work),
// vendor directories, and module download locations all flow through
// `go list`'s standard resolution path.
//
// Falls back to go/build.Default.Import for stdlib packages because
// `go list` for stdlib is unnecessarily expensive and go/build handles
// $GOROOT/src lookups directly without spawning a subprocess.
//
// GOFLAGS is stripped from the subprocess environment so a recursive
// `-toolexec=rewire` doesn't fire on every `go list` invocation,
// which would deadlock the toolexec pipeline.
func resolvePackageDir(importPath string) (string, error) {
	if cached, ok := packageDirCache.Load(importPath); ok {
		switch v := cached.(type) {
		case string:
			return v, nil
		case error:
			return "", v
		}
	}

	dir, err := resolvePackageDirUncached(importPath)
	if err != nil {
		packageDirCache.Store(importPath, err)
		return "", err
	}
	packageDirCache.Store(importPath, dir)
	return dir, nil
}

func resolvePackageDirUncached(importPath string) (string, error) {
	defer profileStage("resolve-pkg-dir", importPath)()

	// Stdlib fast path: go/build resolves these without subprocess
	// overhead. Detect by asking go/build for the package and checking
	// pkg.Goroot — if set, it's in $GOROOT/src and no replace/vendor
	// mechanism applies.
	if pkg, err := build.Default.Import(importPath, ".", build.FindOnly); err == nil && pkg.Goroot && pkg.Dir != "" {
		return pkg.Dir, nil
	}

	// Module-aware path: defer to `go list`, which knows about
	// replace/workspace/vendor.
	cmd := exec.Command("go", "list", "-find", "-f", "{{.Dir}}", importPath)
	cmd.Env = envWithoutGOFLAGS()
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("go list %s: %w\nstderr: %s", importPath, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("go list %s: %w", importPath, err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("go list %s: returned empty directory", importPath)
	}
	return dir, nil
}

// envWithoutGOFLAGS returns the process environment with any GOFLAGS
// entry stripped. Used when shelling out to `go list` / `go build`
// inside a toolexec invocation so the subprocess doesn't recursively
// trigger `-toolexec=rewire`.
func envWithoutGOFLAGS() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "GOFLAGS=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// resolveInterfaceSource is the InterfaceResolver implementation
// plumbed into mockgen.GenerateRewireMock. Given a package import path
// and an interface name, it locates the package's source directory
// (via go list / go/build) and returns the raw bytes of whichever .go
// file in that directory declares the interface.
//
// Used by mockgen to walk embedded interface chains: for an embed like
// `io.Reader`, mockgen asks this resolver for ("io", "Reader") and
// gets back the contents of io/io.go (or whichever file declares
// Reader in the io package).
func resolveInterfaceSource(importPath, interfaceName string) ([]byte, error) {
	pkgDir, err := resolvePackageDir(importPath)
	if err != nil {
		return nil, err
	}
	return readInterfaceSource(pkgDir, interfaceName)
}

// listPackageExportedTypes is the PackageTypeLister implementation
// plumbed into mockgen.GenerateRewireMock. For a given import path,
// it returns the set of all exported top-level type names declared
// in that package's non-test .go files.
//
// Called by mockgen when an interface's declaring file uses
// `import . "pkg"` — mockgen needs to know which bare identifiers in
// the interface's method signatures actually refer to the
// dot-imported package versus the declaring package itself.
func listPackageExportedTypes(importPath string) (map[string]bool, error) {
	pkgDir, err := resolvePackageDir(importPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			// Skip files we can't parse — the exported set is still
			// useful from whatever files we can read. If nothing
			// parses, the caller will get an empty set and the
			// qualifier will behave as if there were no dot-imports.
			continue
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if ts.Name.IsExported() {
					out[ts.Name.Name] = true
				}
			}
		}
	}
	return out, nil
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

// readPackageNameFromArgs finds the first .go source file in the
// compile args and returns its package name. Used to generate
// sidecar files in the same package as the rewritten source.
func readPackageNameFromArgs(args []string) (string, error) {
	for _, arg := range args {
		if !strings.HasSuffix(arg, ".go") {
			continue
		}
		data, err := os.ReadFile(arg)
		if err != nil {
			continue
		}
		// Skip over comments and blank lines to find "package X".
		// A full parser is overkill for a one-line header read.
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if rest, ok := strings.CutPrefix(trimmed, "package "); ok {
				name := strings.TrimSpace(rest)
				if idx := strings.IndexAny(name, " \t/"); idx >= 0 {
					name = name[:idx]
				}
				if name != "" {
					return name, nil
				}
			}
			break
		}
	}
	return "", fmt.Errorf("no package declaration found in any of %d args", len(args))
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

		// Derive package qualifier from import path (last segment).
		// Collisions (e.g. foo/bar vs baz/bar) are broken by suffixing
		// a counter. Walk until we find an unused name so three-way
		// and N-way collisions all produce distinct aliases, and a
		// renamed alias can't accidentally match another package's
		// base name (e.g. a package literally called "bar_2").
		segments := strings.Split(importPath, "/")
		pkgLocalName := segments[len(segments)-1]
		alias := "_rewire_" + pkgLocalName
		for n := 2; usedAliases[alias] > 0; n++ {
			alias = fmt.Sprintf("_rewire_%s_%d", pkgLocalName, n)
		}
		usedAliases[alias] = 1

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

			// Register the per-function goroutine-keyed map used by
			// the parallel-safe dispatch path. The rewriter emits
			// Mock_Foo_ByGoroutine alongside Mock_Foo for every
			// rewritten target (non-generic path only, since
			// ImportPath-driven emission doesn't hit the generic
			// rewriter branches today). Generic functions skip —
			// their dispatch is via sync.Map keyed on type
			// signature, a different parallelism story.
			if !isGenericFunc(e.importPath, fn) {
				fmt.Fprintf(&b, "\trewire.RegisterGoroutineMap(%q, &%s.%s_ByGoroutine)\n",
					e.importPath+"."+fn, e.alias, mockVarName(fn))
			}

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

			// Methods referenced by rewire.InstanceFunc /
			// RestoreInstanceFunc need the per-instance table registered.
			// The rewriter emitted a Mock_Type_Method_ByInstance sync.Map
			// at the same package level.
			//
			// The witness arg is the rewriter's Real_X alias — for
			// non-generic methods that's a single function value, for
			// generic methods on generic types it's instantiated per
			// type-arg combination (one RegisterByInstance call per
			// instantiation, each with the witness instantiated to the
			// matching type args so reflect.TypeFor sees the right
			// signature).
			if byInstance[e.importPath][fn] {
				if isGenericFunc(e.importPath, fn) {
					combos := instantiations[e.importPath][fn]
					for _, typeArgs := range combos {
						fmt.Fprintf(&b, "\trewire.RegisterByInstance(%q, &%s.%s_ByInstance, %s.%s[%s])\n",
							e.importPath+"."+fn, e.alias, mockVarName(fn),
							e.alias, realVarName(fn), strings.Join(typeArgs, ", "))
					}
				} else {
					fmt.Fprintf(&b, "\trewire.RegisterByInstance(%q, &%s.%s_ByInstance, %s.%s)\n",
						e.importPath+"."+fn, e.alias, mockVarName(fn), e.alias, realVarName(fn))
				}
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
//
// Instrumentation flags on the compile invocation (-race, -msan, -asan)
// are propagated to the go list subprocess so the returned archive
// paths match the instrumented flavor the rest of the build uses.
// Without this, a `go test -race` build would mix race-instrumented
// .a files (from the compile step) with non-race .a files (from our
// importcfg patch), which fails at link time with a fingerprint
// mismatch on reflect or sync.
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

	exports, err := resolveStdExportPaths(missing, detectInstrumentationFlags(args))
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

// detectInstrumentationFlags scans compile tool args for -race, -msan,
// -asan and returns whichever are present. These must be propagated
// to any `go list -export` subprocess rewire spawns so the returned
// archive paths match the instrumented flavor the rest of the build
// is using; mixing flavors triggers a link-time fingerprint mismatch.
func detectInstrumentationFlags(args []string) []string {
	var flags []string
	for _, a := range args {
		switch a {
		case "-race", "-msan", "-asan":
			flags = append(flags, a)
		}
	}
	return flags
}

// resolveStdExportPaths runs `go list -export` to find the compiled .a
// files for each package, with GOFLAGS stripped so the recursive
// toolexec doesn't fire. extraFlags (typically -race/-msan/-asan
// detected from the compile invocation) are passed through so the
// archive paths returned match the instrumentation flavor the
// surrounding build is using.
func resolveStdExportPaths(pkgs []string, extraFlags []string) (map[string]string, error) {
	defer profileStage("go-list-export", strings.Join(pkgs, ","))()
	listArgs := append([]string{"list"}, extraFlags...)
	listArgs = append(listArgs, "-export", "-f", "{{.ImportPath}}|{{.Export}}")
	listArgs = append(listArgs, pkgs...)
	cmd := exec.Command("go", listArgs...)
	cmd.Env = envWithoutGOFLAGS()

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

// execTool runs the target tool as a child process and waits for it
// to finish, returning its exit code. Used when rewire still has
// cleanup work to do after the tool exits (e.g. removing temp files
// that were passed to the compile via rewritten args).
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

// execToolReplace replaces the current rewire process with the target
// tool via syscall.Exec. No fork, no parent→child IPC overhead, no
// wait — the rewire process literally becomes the compile process.
// Saves ~10 ms per invocation vs execTool's fork+wait model, which
// matters at scale because Go invokes the toolexec wrapper once per
// compile step.
//
// On success this function does not return: the Exec syscall
// overwrites the current process image. On failure it falls back to
// execTool so the build still progresses (with the old overhead).
//
// MUST NOT be called when there are pending defers that need to
// run — anything after the syscall.Exec is lost. The no-op paths in
// Run() have no cleanup registered, which is why they're safe call
// sites for this; the rewrite path still uses execTool because it
// has deferred temp-dir cleanup.
func execToolReplace(tool string, args []string) int {
	// syscall.Exec expects argv[0] to be the program name. Go's
	// exec.Command does this implicitly; we have to do it manually.
	argv := append([]string{tool}, args...)
	if err := syscall.Exec(tool, argv, os.Environ()); err != nil {
		// Only reached on failure. Fall back to the normal path so
		// the build still progresses, and log once so users can
		// debug if their platform doesn't support Exec.
		fmt.Fprintf(os.Stderr, "rewire: syscall.Exec failed for %s, falling back to fork+wait: %v\n", tool, err)
		return execTool(tool, args)
	}
	// Unreachable — syscall.Exec either replaces the process or
	// returns an error above.
	return 0
}
