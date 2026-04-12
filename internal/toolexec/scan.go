package toolexec

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// mockTargets maps import path → list of function names to mock.
type mockTargets map[string][]string

// loadOrScanMockTargets returns the set of functions that need to be mocked,
// as declared by rewire.Func calls in test files across the module.
//
// Results are cached per build session (keyed on parent PID). A file lock
// ensures only one toolexec process scans; others block until the cache
// is ready.
func loadOrScanMockTargets(moduleRoot string) mockTargets {
	cacheDir := filepath.Join(os.TempDir(), fmt.Sprintf("rewire-%d", os.Getppid()))
	cacheFile := filepath.Join(cacheDir, "mock_targets.json")
	lockFile := filepath.Join(cacheDir, "mock_targets.lock")

	os.MkdirAll(cacheDir, 0755)

	// Acquire file lock — first process scans, others wait
	lock, err := os.Create(lockFile)
	if err != nil {
		// Can't create lock — fall back to scanning without lock
		return scanAllTestFiles(moduleRoot)
	}
	defer lock.Close()

	syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	// Under lock: check if cache was written by another process
	if data, err := os.ReadFile(cacheFile); err == nil {
		var targets mockTargets
		if json.Unmarshal(data, &targets) == nil {
			return targets
		}
	}

	// We're the first — scan and write cache
	targets := scanAllTestFiles(moduleRoot)

	if data, err := json.Marshal(targets); err == nil {
		os.WriteFile(cacheFile, data, 0644)
	}

	return targets
}

// scanAllTestFiles walks the module and finds all rewire.Func calls in test files.
func scanAllTestFiles(moduleRoot string) mockTargets {
	targets := mockTargets{}

	filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fileTargets := scanFileForMockCalls(path)
		for pkg, funcs := range fileTargets {
			targets[pkg] = append(targets[pkg], funcs...)
		}
		return nil
	})

	for pkg, funcs := range targets {
		targets[pkg] = dedupe(funcs)
	}

	return targets
}

// scanFileForMockCalls parses a test file and returns all rewire.Func targets.
func scanFileForMockCalls(path string) mockTargets {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}

	// Build import map: local name → import path
	imports := map[string]string{}
	rewireLocalName := ""
	for _, imp := range f.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		var localName string
		if imp.Name != nil {
			localName = imp.Name.Name
		} else {
			segments := strings.Split(importPath, "/")
			localName = segments[len(segments)-1]
		}
		imports[localName] = importPath
		if importPath == "github.com/GiGurra/rewire/pkg/rewire" {
			rewireLocalName = localName
		}
	}

	if rewireLocalName == "" {
		return nil
	}

	// Walk AST looking for rewire.Func(t, pkg.FuncName, ...) calls
	targets := mockTargets{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != rewireLocalName || sel.Sel.Name != "Func" {
			return true
		}

		if len(call.Args) < 2 {
			return true
		}

		funcSel, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := funcSel.X.(*ast.Ident)
		if !ok {
			return true
		}

		importPath, ok := imports[pkgIdent.Name]
		if !ok {
			return true
		}

		targets[importPath] = append(targets[importPath], funcSel.Sel.Name)
		return true
	})

	return targets
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
