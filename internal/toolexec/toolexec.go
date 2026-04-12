package toolexec

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	targets := loadOrScanMockTargets(moduleRoot)

	funcsToMock := targets[pkgPath]
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

	rewrittenArgs, cleanup, err := rewriteCompileArgs(toolArgs, pkgPath, funcsToMock, isTest, targets)
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
func rewriteCompileArgs(args []string, pkgPath string, funcsToMock []string, isTest bool, allTargets mockTargets) ([]string, func(), error) {
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
				result, err := rewriter.RewriteSource(rewritten, fn)
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
	}

	// For test compilations, generate registration directly from targets
	if isTest {
		regFile, err := generateRegistration(args, allTargets)
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

// generateRegistration creates init() code that registers mock var pointers.
// It works directly from mock targets — no manifests needed.
func generateRegistration(compileArgs []string, targets mockTargets) (string, error) {
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
		}
	}
	b.WriteString("}\n")

	return b.String(), nil
}

// mockVarName converts a target name to the corresponding Mock_ variable name.
// "Func"              → "Mock_Func"
// "(*Server).Handle"  → "Mock_Server_Handle"
// "Point.String"      → "Mock_Point_String"
func mockVarName(targetName string) string {
	name := strings.NewReplacer("(*", "", ")", "", ".", "_").Replace(targetName)
	return "Mock_" + name
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
