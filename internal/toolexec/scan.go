package toolexec

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// mockTargets maps import path → list of function names to mock.
type mockTargets map[string][]string

// genericInstantiations tracks the specific type-argument combinations
// referenced at each rewire call site for generic functions. The
// codegen emits one RegisterReal call per unique instantiation so
// that rewire.Real can return the right concrete real function without
// any runtime generic instantiation (which Go doesn't support).
//
// Shape: importPath -> funcName -> list of type-arg-tuples, each being
// a slice of Go source strings (e.g. ["int", "string"]).
type genericInstantiations map[string]map[string][][]string

// scanCache is the on-disk cache format written by loadOrScanMockTargets.
type scanCache struct {
	Targets       mockTargets           `json:"targets"`
	Instantiations genericInstantiations `json:"instantiations"`
}

// loadOrScanMockTargets returns the set of functions that need to be mocked,
// as declared by rewire.{Func,Real,Restore} calls in test files across the
// module, along with the specific type-argument combinations referenced
// for any generic targets.
//
// Results are cached per build session (keyed on parent PID). A file lock
// ensures only one toolexec process scans; others block until the cache
// is ready.
func loadOrScanMockTargets(moduleRoot string) (mockTargets, genericInstantiations) {
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
		var cache scanCache
		if json.Unmarshal(data, &cache) == nil && cache.Targets != nil {
			return cache.Targets, cache.Instantiations
		}
	}

	// We're the first — scan and write cache
	targets, insts := scanAllTestFiles(moduleRoot)

	if data, err := json.Marshal(scanCache{Targets: targets, Instantiations: insts}); err == nil {
		_ = os.WriteFile(cacheFile, data, 0644)
	}

	return targets, insts
}

// scanAllTestFiles walks the module and finds all rewire.{Func,Real,Restore}
// calls in test files.
func scanAllTestFiles(moduleRoot string) (mockTargets, genericInstantiations) {
	targets := mockTargets{}
	insts := genericInstantiations{}

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

		fileTargets, fileInsts := scanFileForMockCalls(path)
		for pkg, funcs := range fileTargets {
			targets[pkg] = append(targets[pkg], funcs...)
		}
		for pkg, byFunc := range fileInsts {
			if insts[pkg] == nil {
				insts[pkg] = map[string][][]string{}
			}
			for fn, combos := range byFunc {
				insts[pkg][fn] = append(insts[pkg][fn], combos...)
			}
		}
		return nil
	})

	for pkg, funcs := range targets {
		targets[pkg] = dedupe(funcs)
	}
	for pkg, byFunc := range insts {
		for fn, combos := range byFunc {
			insts[pkg][fn] = dedupeTypeArgs(combos)
		}
	}

	return targets, insts
}

func dedupeTypeArgs(combos [][]string) [][]string {
	seen := map[string]bool{}
	var out [][]string
	for _, c := range combos {
		key := strings.Join(c, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// scanFileForMockCalls parses a test file and returns all rewire target
// references, along with any generic type-argument combinations.
func scanFileForMockCalls(path string) (mockTargets, genericInstantiations) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil
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
		return nil, nil
	}

	// Walk AST looking for rewire.Func / rewire.Real / rewire.Restore calls.
	// All three take the target function as their second argument (after t),
	// and any one of them should trigger rewriting so the wrapper and the
	// Real_ alias exist.
	targets := mockTargets{}
	insts := genericInstantiations{}
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

		if typeArgs := extractTypeArgs(call.Args[1], fset); len(typeArgs) > 0 {
			if insts[importPath] == nil {
				insts[importPath] = map[string][][]string{}
			}
			insts[importPath][targetName] = append(insts[importPath][targetName], typeArgs)
		}
		return true
	})

	return targets, insts
}

// extractTypeArgs returns the type-argument expressions of a generic
// reference like pkg.Map[int, string], as Go source strings. Returns
// nil for non-generic references.
func extractTypeArgs(expr ast.Expr, fset *token.FileSet) []string {
	switch idx := expr.(type) {
	case *ast.IndexExpr:
		return []string{exprSource(idx.Index, fset)}
	case *ast.IndexListExpr:
		out := make([]string, 0, len(idx.Indices))
		for _, ix := range idx.Indices {
			out = append(out, exprSource(ix, fset))
		}
		return out
	}
	return nil
}

func exprSource(expr ast.Expr, fset *token.FileSet) string {
	var buf strings.Builder
	_ = printer.Fprint(&buf, fset, expr)
	return buf.String()
}

// extractMockTarget extracts the import path and target name from the second
// argument of a rewire.Func call. Handles:
//   - pkg.Func                  → (importPath, "Func")
//   - pkg.Func[T]               → (importPath, "Func")      (generic, 1 type arg)
//   - pkg.Func[T, U]            → (importPath, "Func")      (generic, 2+ type args)
//   - pkg.Type.Method           → (importPath, "Type.Method")
//   - (*pkg.Type).Method        → (importPath, "(*Type).Method")
//
// For generic references, the type arguments are discarded here — the
// scanner only needs to know which function to rewrite, not which
// instantiations exist. Per-instantiation dispatch happens at runtime
// via reflect.TypeOf keying.
func extractMockTarget(expr ast.Expr, imports map[string]string) (importPath, targetName string) {
	// Strip an optional [T] or [T, U, ...] type-argument list.
	if idx, ok := expr.(*ast.IndexExpr); ok {
		expr = idx.X
	} else if idx, ok := expr.(*ast.IndexListExpr); ok {
		expr = idx.X
	}

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
