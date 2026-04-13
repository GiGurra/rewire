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

		importPath, targetName, typeArgs := extractMockTarget(call.Args[1], imports, fset)
		if importPath == "" {
			return true
		}

		targets[importPath] = append(targets[importPath], targetName)

		if len(typeArgs) > 0 {
			if insts[importPath] == nil {
				insts[importPath] = map[string][][]string{}
			}
			insts[importPath][targetName] = append(insts[importPath][targetName], typeArgs)
		}
		return true
	})

	return targets, insts
}

func exprSource(expr ast.Expr, fset *token.FileSet) string {
	var buf strings.Builder
	_ = printer.Fprint(&buf, fset, expr)
	return buf.String()
}

// stripTypeArgs peels an IndexExpr or IndexListExpr off an expression,
// returning the inner expression and any type arguments found. For a
// non-generic expression it returns (expr, nil).
func stripTypeArgs(expr ast.Expr, fset *token.FileSet) (ast.Expr, []string) {
	switch idx := expr.(type) {
	case *ast.IndexExpr:
		return idx.X, []string{exprSource(idx.Index, fset)}
	case *ast.IndexListExpr:
		out := make([]string, 0, len(idx.Indices))
		for _, ix := range idx.Indices {
			out = append(out, exprSource(ix, fset))
		}
		return idx.X, out
	}
	return expr, nil
}

// extractMockTarget parses the second argument of a rewire.Func /
// rewire.Real / rewire.Restore call into an import path, a canonical
// target name, and (for generic references) the list of type-argument
// source strings. Returns an empty importPath when the expression
// doesn't match any recognized form.
//
// Handles:
//
//	pkg.Func                       → ("pkg", "Func",             nil)
//	pkg.Func[T]                    → ("pkg", "Func",             [T])
//	pkg.Func[T, U]                 → ("pkg", "Func",             [T U])
//	pkg.Type.Method                → ("pkg", "Type.Method",      nil)
//	pkg.Type[T].Method             → ("pkg", "Type.Method",      [T])
//	(*pkg.Type).Method             → ("pkg", "(*Type).Method",   nil)
//	(*pkg.Type[T]).Method          → ("pkg", "(*Type).Method",   [T])
//	(*pkg.Type[T, U]).Method       → ("pkg", "(*Type).Method",   [T U])
//
// The canonical target name never includes the type arguments, because
// per-instantiation dispatch happens at runtime via reflect.TypeOf
// keying. The type arguments are captured separately so the toolexec
// codegen can materialize concrete Real_X[T1, T2] values at compile time.
func extractMockTarget(expr ast.Expr, imports map[string]string, fset *token.FileSet) (importPath, targetName string, typeArgs []string) {
	// Strip outermost [T] / [T, U] — this covers the function-reference
	// forms like pkg.Func[int, string].
	expr, typeArgs = stripTypeArgs(expr, fset)

	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", "", nil
	}
	methodName := sel.Sel.Name

	// Case 1: pkg.Func (possibly generic — but that was stripped above)
	if pkgIdent, ok := sel.X.(*ast.Ident); ok {
		if ip, ok := imports[pkgIdent.Name]; ok {
			return ip, methodName, typeArgs
		}
		return "", "", nil
	}

	// Case 2: pkg.Type.Method or pkg.Type[T].Method (value receiver).
	// The X is either a SelectorExpr (pkg.Type) or an IndexExpr/
	// IndexListExpr wrapping a SelectorExpr (pkg.Type[T]).
	if innerX, innerArgs := stripTypeArgs(sel.X, fset); innerArgs != nil || isValueReceiverSelector(innerX) {
		if innerSel, ok := innerX.(*ast.SelectorExpr); ok {
			if pkgIdent, ok := innerSel.X.(*ast.Ident); ok {
				if ip, ok := imports[pkgIdent.Name]; ok {
					// Type-args belong to the inner receiver, not the outer selector.
					return ip, innerSel.Sel.Name + "." + methodName, innerArgs
				}
			}
			return "", "", nil
		}
	}

	// Case 3: (*pkg.Type).Method or (*pkg.Type[T]).Method (pointer receiver)
	if parenExpr, ok := sel.X.(*ast.ParenExpr); ok {
		if starExpr, ok := parenExpr.X.(*ast.StarExpr); ok {
			// The inner type may be wrapped in IndexExpr / IndexListExpr.
			inner, innerArgs := stripTypeArgs(starExpr.X, fset)
			if innerSel, ok := inner.(*ast.SelectorExpr); ok {
				if pkgIdent, ok := innerSel.X.(*ast.Ident); ok {
					if ip, ok := imports[pkgIdent.Name]; ok {
						return ip, "(*" + innerSel.Sel.Name + ")." + methodName, innerArgs
					}
				}
			}
		}
		return "", "", nil
	}

	return "", "", nil
}

// isValueReceiverSelector returns true iff expr is a SelectorExpr — used
// by extractMockTarget to distinguish pkg.Type.Method from forms that
// can't be a value receiver.
func isValueReceiverSelector(expr ast.Expr) bool {
	_, ok := expr.(*ast.SelectorExpr)
	return ok
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
