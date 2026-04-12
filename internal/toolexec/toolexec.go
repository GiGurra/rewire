package toolexec

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GiGurra/rewire/internal/rewriter"
)

// Run executes the toolexec wrapper. It is called when rewire is invoked as:
//
//	rewire /path/to/go/tool/compile <args...>
//
// Rewriting only happens during 'go test' builds and only for functions
// explicitly referenced in rewire.Func calls. All other compilations
// pass through untouched.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "rewire toolexec: missing tool argument")
		return 1
	}

	tool := args[0]
	toolArgs := args[1:]

	// Only intercept the compile tool during test builds
	if !isCompileTool(tool) {
		return execTool(tool, toolArgs)
	}
	if !isGoTestBuild() {
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

	// Check if this package has any functions to mock
	funcsToMock := targets[pkgPath]
	isTest := hasTestFiles(toolArgs)

	// Reject intrinsic functions early — the compiler replaces calls to these
	// with CPU instructions at the call site, so our wrapper is never invoked
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

	rewrittenArgs, cleanup, err := rewriteCompileArgs(toolArgs, pkgPath, funcsToMock, isTest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rewire: rewrite failed for %s: %v\n", pkgPath, err)
		return 1
	}
	if cleanup != nil {
		defer cleanup()
	}

	return execTool(tool, rewrittenArgs)
}

// isGoTestBuild checks if the parent process is 'go test'.
func isGoTestBuild() bool {
	ppid := os.Getppid()
	out, err := exec.Command("ps", "-p", strconv.Itoa(ppid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	args := strings.Fields(strings.TrimSpace(string(out)))
	if len(args) < 2 {
		return false
	}
	return args[1] == "test"
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

// manifestDir returns a temp directory shared across all toolexec invocations
// in the same 'go test' run, keyed on the parent PID.
func manifestDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("rewire-%d", os.Getppid()))
}

type manifest struct {
	ImportPath  string   `json:"importPath"`
	PackageName string   `json:"packageName"`
	Functions   []string `json:"functions"`
}

func writeManifest(m manifest) error {
	dir := manifestDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	safeName := strings.ReplaceAll(m.ImportPath, "/", "_")
	return os.WriteFile(filepath.Join(dir, safeName+".json"), data, 0644)
}

func readAllManifests() ([]manifest, error) {
	dir := manifestDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []manifest
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || e.Name() == "mock_targets.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		result = append(result, m)
	}
	return result, nil
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
// For test compilations, it also generates a registration file from manifests.
func rewriteCompileArgs(args []string, pkgPath string, funcsToMock []string, isTest bool) ([]string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "rewire-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	newArgs := make([]string, len(args))
	copy(newArgs, args)

	var rewrittenFuncs []string
	var pkgName string

	// Rewrite only the specified functions in non-test source files
	if len(funcsToMock) > 0 {
		for i, arg := range newArgs {
			if !strings.HasSuffix(arg, ".go") || strings.HasSuffix(arg, "_test.go") {
				continue
			}

			src, err := os.ReadFile(arg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("reading %s: %w", arg, err)
			}

			if pkgName == "" {
				pkgName = extractPackageName(src)
			}

			// Only rewrite functions that are in this file
			rewritten := src
			for _, fn := range funcsToMock {
				result, err := rewriter.RewriteSource(rewritten, fn)
				if err != nil {
					// Function might not be in this file — that's OK
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

		// Verify all requested functions were found — if not, fail clearly
		rewrittenSet := map[string]bool{}
		for _, fn := range rewrittenFuncs {
			rewrittenSet[fn] = true
		}
		for _, fn := range funcsToMock {
			if !rewrittenSet[fn] {
				cleanup()
				return nil, nil, fmt.Errorf(
					"function %s.%s cannot be mocked — not found in any source file.\n"+
						"  The function may be implemented in assembly or excluded by build constraints.",
					pkgPath, fn)
			}
		}

		// Write manifest of what was actually rewritten
		if len(rewrittenFuncs) > 0 && pkgName != "" {
			writeManifest(manifest{
				ImportPath:  pkgPath,
				PackageName: pkgName,
				Functions:   rewrittenFuncs,
			})
		}
	}

	// For test compilations, generate registration from manifests
	if isTest {
		regFile, err := generateRegistration(args)
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

// generateRegistration creates init() code that registers mock var pointers
// for all packages that were rewritten during this build.
func generateRegistration(compileArgs []string) (string, error) {
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

	manifests, err := readAllManifests()
	if err != nil {
		return "", fmt.Errorf("reading manifests: %w", err)
	}

	type entry struct {
		importPath string
		alias      string
		funcNames  []string
	}
	var entries []entry
	usedAliases := map[string]int{}

	for _, m := range manifests {
		if !allImports[m.ImportPath] || len(m.Functions) == 0 {
			continue
		}
		if m.ImportPath == "github.com/GiGurra/rewire/pkg/rewire" {
			continue
		}

		alias := "_rewire_" + m.PackageName
		if count := usedAliases[alias]; count > 0 {
			alias = fmt.Sprintf("_rewire_%s_%d", m.PackageName, count+1)
		}
		usedAliases[alias]++

		entries = append(entries, entry{
			importPath: m.ImportPath,
			alias:      alias,
			funcNames:  m.Functions,
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
			fmt.Fprintf(&b, "\trewire.Register(%q, &%s.Mock_%s)\n",
				e.importPath+"."+fn, e.alias, fn)
		}
	}
	b.WriteString("}\n")

	return b.String(), nil
}

func extractPackageName(src []byte) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.PackageClauseOnly)
	if err != nil {
		return ""
	}
	return f.Name.Name
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
