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

	"github.com/gofrs/flock"
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
	lockPath := filepath.Join(cacheDir, "mock_targets.lock")

	_ = os.MkdirAll(cacheDir, 0755)

	// Acquire file lock — first process scans, others wait
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		// Can't acquire lock — fall back to scanning without lock
		return scanAllTestFiles(moduleRoot)
	}
	defer func() { _ = fl.Unlock() }()

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
		_ = os.WriteFile(cacheFile, data, 0644)
	}

	return targets
}

// scanAllTestFiles walks the module and finds all rewire.Func calls in test files.
func scanAllTestFiles(moduleRoot string) mockTargets {
	targets := mockTargets{}

	_ = filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
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

	// Walk AST looking for rewire.Func / rewire.Real / rewire.Restore calls.
	// All three take the target function as their second argument (after t),
	// and any one of them should trigger rewriting so the wrapper and the
	// Real_ alias exist.
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
		if !ok || ident.Name != rewireLocalName {
			return true
		}
		switch sel.Sel.Name {
		case "Func", "Real", "Restore":
			// process below
		default:
			return true
		}

		if len(call.Args) < 2 {
			return true
		}

		importPath, targetName := extractMockTarget(call.Args[1], imports)
		if importPath == "" {
			return true
		}

		targets[importPath] = append(targets[importPath], targetName)
		return true
	})

	return targets
}

// extractMockTarget extracts the import path and target name from the second
// argument of a rewire.Func call. Handles:
//   - pkg.Func           → (importPath, "Func")
//   - pkg.Type.Method    → (importPath, "Type.Method")
//   - (*pkg.Type).Method → (importPath, "(*Type).Method")
func extractMockTarget(expr ast.Expr, imports map[string]string) (importPath, targetName string) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	methodName := sel.Sel.Name

	// Case 1: pkg.Func
	if pkgIdent, ok := sel.X.(*ast.Ident); ok {
		if ip, ok := imports[pkgIdent.Name]; ok {
			return ip, methodName
		}
		return "", ""
	}

	// Case 2: pkg.Type.Method (value receiver)
	if innerSel, ok := sel.X.(*ast.SelectorExpr); ok {
		if pkgIdent, ok := innerSel.X.(*ast.Ident); ok {
			if ip, ok := imports[pkgIdent.Name]; ok {
				return ip, innerSel.Sel.Name + "." + methodName
			}
		}
		return "", ""
	}

	// Case 3: (*pkg.Type).Method (pointer receiver)
	if parenExpr, ok := sel.X.(*ast.ParenExpr); ok {
		if starExpr, ok := parenExpr.X.(*ast.StarExpr); ok {
			if innerSel, ok := starExpr.X.(*ast.SelectorExpr); ok {
				if pkgIdent, ok := innerSel.X.(*ast.Ident); ok {
					if ip, ok := imports[pkgIdent.Name]; ok {
						return ip, "(*" + innerSel.Sel.Name + ")." + methodName
					}
				}
			}
		}
		return "", ""
	}

	return "", ""
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
