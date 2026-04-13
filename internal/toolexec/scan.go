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

// byInstanceTargets identifies (importPath, targetName) pairs that are
// referenced by at least one rewire.InstanceMethod or
// rewire.RestoreInstanceMethod call anywhere in the module. The rewriter
// emits an extra per-instance dispatch path for these, and the codegen
// emits a RegisterByInstance call so that rewire.InstanceMethod can
// resolve the per-instance sync.Map.
//
// Shape: importPath -> targetName -> true.
type byInstanceTargets map[string]map[string]bool

// mockedInterfaces lists interface types referenced by rewire.NewMock[I]
// calls anywhere in the test module. The toolexec codegen emits a
// backing struct + factory registration for each one at test compile
// time, eliminating the go:generate / committed-mock-file workflow.
//
// Shape: importPath -> interface-type-name -> true.
type mockedInterfaces map[string]map[string]bool

// scanCache is the on-disk cache format written by loadOrScanMockTargets.
type scanCache struct {
	Targets          mockTargets           `json:"targets"`
	Instantiations   genericInstantiations `json:"instantiations"`
	ByInstance       byInstanceTargets     `json:"byInstance"`
	MockedInterfaces mockedInterfaces      `json:"mockedInterfaces"`
}

// loadOrScanMockTargets returns the set of functions that need to be mocked,
// as declared by rewire.{Func,Real,Restore,InstanceMethod,RestoreInstanceMethod,NewMock}
// calls in test files across the module, along with the specific
// type-argument combinations referenced for any generic targets, the
// subset of targets that need a per-instance dispatch path, and the set
// of interface types referenced by rewire.NewMock[I] for which the
// codegen should emit a backing struct.
//
// Results are cached per build session (keyed on parent PID). A file lock
// ensures only one toolexec process scans; others block until the cache
// is ready.
func loadOrScanMockTargets(moduleRoot string) (mockTargets, genericInstantiations, byInstanceTargets, mockedInterfaces) {
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
			return cache.Targets, cache.Instantiations, cache.ByInstance, cache.MockedInterfaces
		}
	}

	// We're the first — scan and write cache
	targets, insts, byInst, mockedIfaces := scanAllTestFiles(moduleRoot)

	cache := scanCache{
		Targets:          targets,
		Instantiations:   insts,
		ByInstance:       byInst,
		MockedInterfaces: mockedIfaces,
	}
	if data, err := json.Marshal(cache); err == nil {
		_ = os.WriteFile(cacheFile, data, 0644)
	}

	return targets, insts, byInst, mockedIfaces
}

// scanAllTestFiles walks the module and finds all rewire.{Func,Real,Restore,
// InstanceMethod,RestoreInstanceMethod,NewMock} calls in test files.
//
// After the walk, interface-typed targets are filtered out of
// targets/byInstance for the interface's declaring package — those
// interfaces are mocked via the codegen path, not via rewriter-level
// method rewriting.
func scanAllTestFiles(moduleRoot string) (mockTargets, genericInstantiations, byInstanceTargets, mockedInterfaces) {
	targets := mockTargets{}
	insts := genericInstantiations{}
	byInst := byInstanceTargets{}
	mockedIfaces := mockedInterfaces{}

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

		fileTargets, fileInsts, fileByInst, fileMockedIfaces := scanFileForMockCalls(path)
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
		for pkg, names := range fileByInst {
			if byInst[pkg] == nil {
				byInst[pkg] = map[string]bool{}
			}
			for name := range names {
				byInst[pkg][name] = true
			}
		}
		for pkg, ifaces := range fileMockedIfaces {
			if mockedIfaces[pkg] == nil {
				mockedIfaces[pkg] = map[string]bool{}
			}
			for iface := range ifaces {
				mockedIfaces[pkg][iface] = true
			}
		}
		return nil
	})

	// Filter out InstanceMethod / RestoreInstanceMethod targets whose
	// receiver type is a mocked interface. Those targets don't get
	// rewritten in the interface's declaring package — the per-instance
	// dispatch table is emitted into the test package instead, by the
	// mock struct codegen.
	for pkg, ifaces := range mockedIfaces {
		if _, ok := targets[pkg]; !ok {
			continue
		}
		filteredTargets := targets[pkg][:0]
		for _, name := range targets[pkg] {
			typeName, _, isMethod := parseTargetName(name)
			if isMethod && ifaces[typeName] {
				continue // skip — handled by codegen
			}
			filteredTargets = append(filteredTargets, name)
		}
		targets[pkg] = filteredTargets
		if byInst[pkg] != nil {
			for name := range byInst[pkg] {
				typeName, _, isMethod := parseTargetName(name)
				if isMethod && ifaces[typeName] {
					delete(byInst[pkg], name)
				}
			}
		}
	}

	for pkg, funcs := range targets {
		targets[pkg] = dedupe(funcs)
	}
	for pkg, byFunc := range insts {
		for fn, combos := range byFunc {
			insts[pkg][fn] = dedupeTypeArgs(combos)
		}
	}

	return targets, insts, byInst, mockedIfaces
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
// references, any generic type-argument combinations, the subset of
// targets that need a per-instance dispatch path, and the set of
// interface types referenced by rewire.NewMock[I].
func scanFileForMockCalls(path string) (mockTargets, genericInstantiations, byInstanceTargets, mockedInterfaces) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil, nil, nil
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
		return nil, nil, nil, nil
	}

	// Walk AST looking for rewire.{Func,Real,Restore,InstanceMethod,
	// RestoreInstanceMethod,NewMock} calls.
	//
	// Func/Real/Restore take the target function as their second argument
	// (index 1), after t.
	//
	// InstanceMethod/RestoreInstanceMethod take an instance as their second
	// argument (index 1) and the target as their third (index 2) — and they
	// additionally flag the target as needing per-instance emission.
	//
	// NewMock[I](...) takes its type argument as an IndexExpr wrapping
	// the rewire.NewMock selector — no positional target argument.
	targets := mockTargets{}
	insts := genericInstantiations{}
	byInst := byInstanceTargets{}
	mockedIfaces := mockedInterfaces{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// rewire.NewMock[I] is an IndexExpr (or IndexListExpr for
		// multi-type-param generic interfaces in a future phase),
		// wrapping rewire.NewMock and carrying I as its index.
		if inner, idxArgs := stripTypeArgs(call.Fun, fset); len(idxArgs) > 0 {
			if sel, ok := inner.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == rewireLocalName && sel.Sel.Name == "NewMock" {
					// Re-parse the type argument as a selector expression
					// (pkg.Iface) since stripTypeArgs returned it as a string.
					// Walk the IndexExpr's Index directly to get the AST.
					switch idx := call.Fun.(type) {
					case *ast.IndexExpr:
						if importPath, typeName := extractInterfaceRef(idx.Index, imports); importPath != "" {
							if mockedIfaces[importPath] == nil {
								mockedIfaces[importPath] = map[string]bool{}
							}
							mockedIfaces[importPath][typeName] = true
						}
					case *ast.IndexListExpr:
						// Future: generic interfaces. For now ignore.
					}
					return true
				}
			}
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != rewireLocalName {
			return true
		}

		var targetArgIdx int
		var needsByInstance bool
		switch sel.Sel.Name {
		case "Func", "Real", "Restore":
			targetArgIdx = 1
		case "InstanceMethod", "RestoreInstanceMethod":
			targetArgIdx = 2
			needsByInstance = true
		default:
			return true
		}

		if len(call.Args) <= targetArgIdx {
			return true
		}

		importPath, targetName, typeArgs := extractMockTarget(call.Args[targetArgIdx], imports, fset)
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

		if needsByInstance {
			if byInst[importPath] == nil {
				byInst[importPath] = map[string]bool{}
			}
			byInst[importPath][targetName] = true
		}
		return true
	})

	return targets, insts, byInst, mockedIfaces
}

// extractInterfaceRef resolves an expression like `bar.GreeterIface`
// used as a type argument to rewire.NewMock into (importPath,
// typeName). Returns ("","") if the expression isn't a recognized
// package-qualified identifier.
func extractInterfaceRef(expr ast.Expr, imports map[string]string) (string, string) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	importPath, ok := imports[pkgIdent.Name]
	if !ok {
		return "", ""
	}
	return importPath, sel.Sel.Name
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
