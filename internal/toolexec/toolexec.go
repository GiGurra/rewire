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
// For compile invocations targeting same-module packages, it rewrites source
// files to add mock variables, writes the rewritten files to a temp directory,
// and invokes the real compiler with the modified file paths.
//
// For test compilations, it also generates a registration file that connects
// mock variables to the rewire registry, enabling the rewire.Func API.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "rewire toolexec: missing tool argument")
		return 1
	}

	tool := args[0]
	toolArgs := args[1:]

	// Only intercept the compile tool
	if !isCompileTool(tool) {
		return execTool(tool, toolArgs)
	}

	modulePath, moduleRoot := findModuleInfo()
	if modulePath == "" {
		return execTool(tool, toolArgs)
	}

	pkgPath := findFlag(toolArgs, "-p")
	if pkgPath == "" || !isInModule(pkgPath, modulePath) {
		return execTool(tool, toolArgs)
	}

	// Rewrite source files in the compile args
	rewrittenArgs, cleanup, err := rewriteCompileArgs(toolArgs, pkgPath, modulePath, moduleRoot)
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
	base := filepath.Base(tool)
	return base == "compile"
}

func isInModule(pkgPath, modulePath string) bool {
	return pkgPath == modulePath || strings.HasPrefix(pkgPath, modulePath+"/")
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

// findFlag returns the value of a flag like "-p foo" from the args.
func findFlag(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// rewriteCompileArgs scans compile args for .go source files, rewrites them,
// and returns modified args pointing to the rewritten temp files.
// For test compilations, it also generates a registration file.
func rewriteCompileArgs(args []string, pkgPath, modulePath, moduleRoot string) ([]string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "rewire-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	newArgs := make([]string, len(args))
	copy(newArgs, args)

	isTest := false
	for _, arg := range args {
		if strings.HasSuffix(arg, "_test.go") {
			isTest = true
			break
		}
	}

	// Rewrite non-test source files
	for i, arg := range newArgs {
		if !strings.HasSuffix(arg, ".go") {
			continue
		}
		if strings.HasSuffix(arg, "_test.go") {
			continue
		}

		src, err := os.ReadFile(arg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("reading %s: %w", arg, err)
		}

		rewritten, err := rewriter.RewriteAllExported(src)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("rewriting %s: %w", arg, err)
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

	// For test compilations, generate a registration file
	if isTest {
		regFile, err := generateRegistration(args, pkgPath, modulePath, moduleRoot)
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

// generateRegistration creates a Go source file with init() that registers
// mock variables for all imported same-module packages.
func generateRegistration(compileArgs []string, testPkgPath, modulePath, moduleRoot string) (string, error) {
	// Find the test package name from source files
	pkgName := ""
	// Collect all same-module imports from the source files
	sameModuleImports := map[string]bool{}

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
			importPath := strings.Trim(imp.Path.Value, `"`)
			if isInModule(importPath, modulePath) {
				sameModuleImports[importPath] = true
			}
		}
	}

	if len(sameModuleImports) == 0 || pkgName == "" {
		return "", nil
	}

	// For each imported same-module package, find its exported functions
	type mockEntry struct {
		importPath string
		importName string // last segment, used as package qualifier
		funcNames  []string
	}
	var entries []mockEntry

	for importPath := range sameModuleImports {
		// Skip the rewire library itself
		if strings.HasPrefix(importPath, "github.com/GiGurra/rewire/pkg/") ||
			strings.HasPrefix(importPath, "github.com/GiGurra/rewire/internal/") {
			continue
		}

		relPath := strings.TrimPrefix(importPath, modulePath)
		relPath = strings.TrimPrefix(relPath, "/")
		pkgDir := filepath.Join(moduleRoot, relPath)

		funcNames, err := listExportedFunctionsInDir(pkgDir)
		if err != nil || len(funcNames) == 0 {
			continue
		}

		// Derive the package qualifier (last path segment)
		segments := strings.Split(importPath, "/")
		qualifier := segments[len(segments)-1]

		entries = append(entries, mockEntry{
			importPath: importPath,
			importName: qualifier,
			funcNames:  funcNames,
		})
	}

	if len(entries) == 0 {
		return "", nil
	}

	// Generate the registration source
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	b.WriteString("import (\n")
	b.WriteString("\t\"github.com/GiGurra/rewire/pkg/rewire\"\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\t%q\n", e.importPath)
	}
	b.WriteString(")\n\n")
	b.WriteString("func init() {\n")
	for _, e := range entries {
		for _, fn := range e.funcNames {
			fmt.Fprintf(&b, "\trewire.Register(%q, &%s.Mock_%s)\n",
				e.importPath+"."+fn, e.importName, fn)
		}
	}
	b.WriteString("}\n")

	return b.String(), nil
}

// listExportedFunctionsInDir reads all .go files in a directory and returns
// the names of exported functions eligible for mocking.
func listExportedFunctionsInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var all []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		funcs, err := rewriter.ListExportedFunctions(src)
		if err != nil {
			continue
		}
		for _, fn := range funcs {
			if !seen[fn] {
				seen[fn] = true
				all = append(all, fn)
			}
		}
	}
	return all, nil
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
