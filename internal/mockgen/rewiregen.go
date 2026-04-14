package mockgen

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// GenerateRewireMock produces a Go source file for the test package
// `outputPkg` that declares a concrete backing struct implementing the
// interface `interfaceName` (resolved from `src`). The generated
// backing struct uses per-instance dispatch via package-level
// sync.Maps and registers itself with the rewire runtime via init()
// so that rewire.NewMock[I] and rewire.InstanceMethod work on it.
//
// The generated file is injected into the test package's compile args
// by the toolexec wrapper — it never lives on disk, never appears in
// go generate output, and has no effect on production source.
//
// Phase 2a scope: non-generic interfaces AND generic interfaces with
// concrete type-argument instantiations. Embedded interfaces and types
// from the interface's own declaring package are still rejected.
//
// Inputs:
//   - src: source bytes of the file in the declaring package that
//     contains the interface declaration. Imports in src are resolved
//     locally to build the method-signature import set.
//   - interfaceName: the bare name of the interface (e.g. "GreeterIface").
//   - interfacePkgPath: the full import path of the declaring package
//     (e.g. "github.com/example/bar").
//   - interfacePkgAlias: the identifier used in the generated file to
//     refer to the interface's package. Typically the default package
//     name (last segment of the import path); the caller chooses this
//     to avoid collisions with the test package's own imports.
//   - outputPkg: the test package name to emit.
//   - typeArgs: the type-argument source strings for the instantiation
//     this mock represents (e.g. ["int"] for ContainerIface[int]).
//     Empty / nil for non-generic interfaces. Each entry is a printed
//     Go source expression as the user wrote it in the test file
//     (e.g. "context.Context", "*time.Time").
func GenerateRewireMock(src []byte, interfaceName, interfacePkgPath, interfacePkgAlias, outputPkg string, typeArgs []string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	var iface *ast.InterfaceType
	var ifaceTypeParams *ast.FieldList
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != interfaceName {
				continue
			}
			ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				return nil, fmt.Errorf("%q is not an interface", interfaceName)
			}
			iface = ifaceType
			ifaceTypeParams = typeSpec.TypeParams
		}
	}
	if iface == nil {
		return nil, fmt.Errorf("interface %q not found", interfaceName)
	}

	// Validate type-arg arity matches the interface's declared
	// type parameters.
	declaredTypeParams := 0
	if ifaceTypeParams != nil {
		declaredTypeParams = ifaceTypeParams.NumFields()
	}
	if declaredTypeParams != len(typeArgs) {
		if declaredTypeParams == 0 {
			return nil, fmt.Errorf("interface %q is not generic but received type arguments %v", interfaceName, typeArgs)
		}
		return nil, fmt.Errorf("interface %q expects %d type argument(s) but received %d (%v)", interfaceName, declaredTypeParams, len(typeArgs), typeArgs)
	}

	// Build the type-parameter substitution map: T -> "int", U -> "string", etc.
	// Each value is the type-argument source string as the user wrote
	// it in the test file. Substitution happens by walking method
	// signatures and replacing any *ast.Ident referencing a type
	// parameter with a parsed expression of the type-arg source.
	typeParamSubst := map[string]ast.Expr{}
	if declaredTypeParams > 0 {
		idx := 0
		for _, field := range ifaceTypeParams.List {
			for _, name := range field.Names {
				argExpr, parseErr := parser.ParseExpr(typeArgs[idx])
				if parseErr != nil {
					return nil, fmt.Errorf("parsing type argument %q for interface %q: %w", typeArgs[idx], interfaceName, parseErr)
				}
				typeParamSubst[name.Name] = argExpr
				idx++
			}
		}
	}

	// Build import map from the declaring package's file: local name → import path.
	// Used to emit only the imports that are actually referenced in method signatures.
	imports := map[string]string{}
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		var localName string
		if imp.Name != nil {
			localName = imp.Name.Name
		} else {
			segments := strings.Split(importPath, "/")
			localName = segments[len(segments)-1]
		}
		imports[localName] = importPath
	}

	type method struct {
		name          string
		params        string // "name string, age int"
		results       string // "(string, error)" or "string" or ""
		namedResults  string // "(_r0 string)" — for zero-return bare-return style
		paramNames    string // "name, age"
		hasResults    bool
		mockFnType    string // user-replacement func type: "func(bar.GreeterIface, string) string"
	}

	var methods []method
	usedPkgs := map[string]bool{}

	for _, field := range iface.Methods.List {
		funcType, ok := field.Type.(*ast.FuncType)
		if !ok || len(field.Names) == 0 {
			return nil, fmt.Errorf("interface %q has an embedded interface or non-method field — embedded interfaces are not yet supported", interfaceName)
		}
		methodName := field.Names[0].Name

		// For generic interfaces, substitute type-parameter references
		// in the method signature with the concrete type arguments
		// before printing. This produces a method declaration that
		// matches what Go's type system materializes for the
		// instantiation. For non-generic interfaces, substituteFuncType
		// returns the original FuncType unchanged.
		instantiatedFuncType := substituteFuncType(funcType, typeParamSubst)
		isVariadic := isVariadicFunc(instantiatedFuncType)
		params := ensureParamNames(instantiatedFuncType.Params)

		paramsSrc := fieldListToString(fset, params)
		paramNamesSrc := buildCallArgs(params, isVariadic)
		hasResults := instantiatedFuncType.Results != nil && len(instantiatedFuncType.Results.List) > 0
		resultsSrc := ""
		namedResultsSrc := ""
		if hasResults {
			resultsSrc = resultsToString(fset, instantiatedFuncType.Results)
			namedResultsSrc = addResultNames(fset, resultsSrc)
		}

		// Track packages referenced in the (post-substitution) method
		// signature so we can emit only the imports we actually need.
		// Substitution might have brought in package selectors from
		// the test file's type-arg expressions (e.g. context.Context).
		collectPkgRefs(instantiatedFuncType, usedPkgs)

		// The replacement function type, seen from the test author's
		// perspective: receiver is the interface type (with alias and
		// instantiated type args, if any), followed by the method's
		// parameters (types only).
		replRecv := interfacePkgAlias + "." + interfaceName
		if len(typeArgs) > 0 {
			replRecv += "[" + strings.Join(typeArgs, ", ") + "]"
		}
		paramTypesOnly := typeOnlyFieldList(fset, params)
		mockFnParams := replRecv
		if paramTypesOnly != "" {
			mockFnParams += ", " + paramTypesOnly
		}
		mockFnType := "func(" + mockFnParams + ")"
		if resultsSrc != "" {
			mockFnType += " " + resultsSrc
		}

		methods = append(methods, method{
			name:         methodName,
			params:       paramsSrc,
			results:      resultsSrc,
			namedResults: namedResultsSrc,
			paramNames:   paramNamesSrc,
			hasResults:   hasResults,
			mockFnType:   mockFnType,
		})
	}

	// Stable iteration order for deterministic output.
	sort.SliceStable(methods, func(i, j int) bool { return methods[i].name < methods[j].name })

	// Derive synthetic names from (interfacePkgAlias, interfaceName,
	// type-args). All identifiers use ASCII-only characters compatible
	// with Go syntax. For generic instantiations the mangled type
	// arguments disambiguate the struct, e.g.:
	//
	//   _rewire_mock_bar_GreeterIface         (non-generic)
	//   _rewire_mock_bar_ContainerIface_int   (Container[int])
	//   _rewire_mock_bar_CacheIface_string_int (Cache[string, int])
	structName := "_rewire_mock_" + interfacePkgAlias + "_" + interfaceName
	if len(typeArgs) > 0 {
		structName += "_" + mangleTypeArgsForIdent(typeArgs)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", outputPkg)

	// Imports.
	var usedImports []string
	addImport := func(path, alias string) {
		defaultAlias := defaultPkgAlias(path)
		if alias == "" || alias == defaultAlias {
			usedImports = append(usedImports, fmt.Sprintf("%q", path))
		} else {
			usedImports = append(usedImports, fmt.Sprintf("%s %q", alias, path))
		}
	}
	addImport("sync", "")
	addImport("github.com/GiGurra/rewire/pkg/rewire", "")
	addImport(interfacePkgPath, interfacePkgAlias)
	// Imports referenced by method signatures, minus the interface's own
	// package alias (which we already added) and minus local-package
	// references (none expected here since we operate at the file level).
	for localName := range usedPkgs {
		if localName == interfacePkgAlias {
			continue
		}
		if path, ok := imports[localName]; ok {
			addImport(path, localName)
		}
	}
	sort.Strings(usedImports)
	b.WriteString("import (\n")
	for _, imp := range usedImports {
		fmt.Fprintf(&b, "\t%s\n", imp)
	}
	b.WriteString(")\n\n")

	// Backing struct. The single non-zero-size padding field is
	// load-bearing — Go's spec explicitly allows pointers to distinct
	// zero-size variables to compare equal, so an empty struct lets
	// the runtime coalesce multiple &mock{} allocations to the same
	// address. That breaks per-instance dispatch, which keys on the
	// receiver pointer. The [1]byte forces distinct allocations to get
	// distinct addresses.
	fmt.Fprintf(&b, "type %s struct{ _ [1]byte }\n\n", structName)

	// Per-method per-instance dispatch tables.
	for _, m := range methods {
		fmt.Fprintf(&b, "var Mock_%s_%s_ByInstance sync.Map\n", structName, m.name)
	}
	b.WriteString("\n")

	// Method implementations. For each method, the body:
	//   1. Looks up the receiver in the per-instance table.
	//   2. Type-asserts the stored replacement to the mock-fn type
	//      (receiver-first, matching the interface method expression).
	//   3. Invokes with the concrete receiver (implicitly converted to
	//      the interface type by Go's assignability rules).
	//   4. Falls back to zero return values if nothing is set.
	for _, m := range methods {
		if m.hasResults {
			fmt.Fprintf(&b, "func (m *%s) %s(%s) %s {\n", structName, m.name, m.params, m.namedResults)
			fmt.Fprintf(&b, "\tif _rewire_raw, _rewire_ok := Mock_%s_%s_ByInstance.Load(m); _rewire_ok {\n", structName, m.name)
			fmt.Fprintf(&b, "\t\tif _rewire_fn, _rewire_ok := _rewire_raw.(%s); _rewire_ok {\n", m.mockFnType)
			fmt.Fprintf(&b, "\t\t\treturn _rewire_fn(m%s)\n", commaPrepend(m.paramNames))
			b.WriteString("\t\t}\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn\n")
		} else {
			fmt.Fprintf(&b, "func (m *%s) %s(%s) {\n", structName, m.name, m.params)
			fmt.Fprintf(&b, "\tif _rewire_raw, _rewire_ok := Mock_%s_%s_ByInstance.Load(m); _rewire_ok {\n", structName, m.name)
			fmt.Fprintf(&b, "\t\tif _rewire_fn, _rewire_ok := _rewire_raw.(%s); _rewire_ok {\n", m.mockFnType)
			fmt.Fprintf(&b, "\t\t\t_rewire_fn(m%s)\n", commaPrepend(m.paramNames))
			b.WriteString("\t\t\treturn\n")
			b.WriteString("\t\t}\n")
			b.WriteString("\t}\n")
		}
		b.WriteString("}\n\n")
	}

	// init(): register the factory, then each per-method dispatch table.
	//
	// The factory registration uses the generic form — the type
	// parameter flows the interface (instantiated for generics) through
	// to reflect at runtime, which derives the registry key from
	// PkgPath()+"."+Name(). Same derivation NewMock[I] uses at lookup
	// time, so the keys can never drift. The generated file doesn't
	// need to import reflect.
	//
	// For generic instantiations the type parameter is the instantiated
	// form, e.g. RegisterMockFactory[bar.ContainerIface[int]](...). At
	// runtime reflect.TypeFor produces "ContainerIface[int]" as the
	// type's Name(), so each instantiation gets a distinct factory.
	fullIfaceName := interfacePkgPath + "." + interfaceName
	instantiatedIface := interfacePkgAlias + "." + interfaceName
	if len(typeArgs) > 0 {
		instantiatedIface += "[" + strings.Join(typeArgs, ", ") + "]"
	}
	b.WriteString("func init() {\n")
	fmt.Fprintf(&b, "\trewire.RegisterMockFactory[%s](func() any { return &%s{} })\n",
		instantiatedIface, structName)
	// RegisterByInstance takes a witness value (last arg) for type
	// inference. For interface mocks we don't have a Real_X variable to
	// borrow, so we emit a typed-nil function value of the right type
	// (mockFnType). The witness is never used at runtime — only its
	// static type matters, which reflect.TypeFor[F] picks up.
	for _, m := range methods {
		fmt.Fprintf(&b, "\trewire.RegisterByInstance(%q, &Mock_%s_%s_ByInstance, (%s)(nil))\n",
			fullIfaceName+"."+m.name, structName, m.name, m.mockFnType)
	}
	b.WriteString("}\n")

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("formatting generated rewire mock (this is a bug in rewire):\n%s\nerror: %w", b.String(), err)
	}
	return formatted, nil
}

// commaPrepend returns ", s" if s is non-empty, or "" otherwise.
func commaPrepend(s string) string {
	if s == "" {
		return ""
	}
	return ", " + s
}

// defaultPkgAlias returns the default Go local name for an import path
// (its last path segment).
func defaultPkgAlias(path string) string {
	segments := strings.Split(path, "/")
	return segments[len(segments)-1]
}

// mangleTypeArgsForIdent renders a list of type-arg source strings as
// a Go-identifier-safe suffix. Used to disambiguate generated struct
// names per generic interface instantiation:
//
//	["int"]                 → "int"
//	["string", "int"]       → "string_int"
//	["*time.Time"]          → "ptr_time_Time"
//	["context.Context"]     → "context_Context"
//	["map[string]int"]      → "map_string_int_"
//
// The mangling is intentionally lossy — distinct user-visible types
// can collide if they share the same mangled form (e.g. `map[K]V` and
// `[]V` once stripped). For Phase 2a's scope (builtin/imported type
// args) collisions are very unlikely in practice; if they occur, the
// generated file would fail to compile because two structs with the
// same name would be declared, which surfaces the problem clearly.
func mangleTypeArgsForIdent(typeArgs []string) string {
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
	return r.Replace(strings.Join(typeArgs, "_"))
}

// substituteFuncType returns a deep-enough copy of funcType with any
// *ast.Ident references to names in subst replaced by the
// corresponding substitution expression. Used to instantiate generic
// interface methods at compile time: for `Add(v T)` with subst T→int,
// the result is `Add(v int)`.
//
// For non-generic interfaces (subst empty) this returns funcType
// unchanged — no allocation, no walk.
//
// The walk handles all type expression forms used in method
// signatures: identifiers, pointers, slices, maps, channels, function
// types, ellipsis (variadic), generic instantiations (IndexExpr /
// IndexListExpr), and qualified selectors. Selectors like
// `context.Context` are passed through unchanged because their X
// component is a package identifier, not a type parameter.
func substituteFuncType(funcType *ast.FuncType, subst map[string]ast.Expr) *ast.FuncType {
	if len(subst) == 0 {
		return funcType
	}
	return &ast.FuncType{
		Func:    funcType.Func,
		Params:  substFieldList(funcType.Params, subst),
		Results: substFieldList(funcType.Results, subst),
	}
}

func substFieldList(fl *ast.FieldList, subst map[string]ast.Expr) *ast.FieldList {
	if fl == nil {
		return nil
	}
	newList := make([]*ast.Field, len(fl.List))
	for i, f := range fl.List {
		newField := *f
		newField.Type = substTypeExpr(f.Type, subst)
		newList[i] = &newField
	}
	return &ast.FieldList{Opening: fl.Opening, Closing: fl.Closing, List: newList}
}

func substTypeExpr(t ast.Expr, subst map[string]ast.Expr) ast.Expr {
	switch e := t.(type) {
	case *ast.Ident:
		if r, ok := subst[e.Name]; ok {
			return r
		}
		return e
	case *ast.StarExpr:
		return &ast.StarExpr{Star: e.Star, X: substTypeExpr(e.X, subst)}
	case *ast.ArrayType:
		return &ast.ArrayType{Lbrack: e.Lbrack, Len: e.Len, Elt: substTypeExpr(e.Elt, subst)}
	case *ast.MapType:
		return &ast.MapType{
			Map:   e.Map,
			Key:   substTypeExpr(e.Key, subst),
			Value: substTypeExpr(e.Value, subst),
		}
	case *ast.ChanType:
		return &ast.ChanType{
			Begin: e.Begin,
			Arrow: e.Arrow,
			Dir:   e.Dir,
			Value: substTypeExpr(e.Value, subst),
		}
	case *ast.FuncType:
		return substituteFuncType(e, subst)
	case *ast.Ellipsis:
		return &ast.Ellipsis{Ellipsis: e.Ellipsis, Elt: substTypeExpr(e.Elt, subst)}
	case *ast.IndexExpr:
		return &ast.IndexExpr{
			X:      e.X,
			Lbrack: e.Lbrack,
			Index:  substTypeExpr(e.Index, subst),
			Rbrack: e.Rbrack,
		}
	case *ast.IndexListExpr:
		newIndices := make([]ast.Expr, len(e.Indices))
		for i, ix := range e.Indices {
			newIndices[i] = substTypeExpr(ix, subst)
		}
		return &ast.IndexListExpr{
			X:       e.X,
			Lbrack:  e.Lbrack,
			Indices: newIndices,
			Rbrack:  e.Rbrack,
		}
	case *ast.SelectorExpr:
		// pkg.Type — a qualified type from another package. The X is a
		// package identifier, not a type parameter, so we leave the
		// selector untouched.
		return e
	}
	// Unknown form (interface literal, struct literal, etc.) — return
	// as-is. The compiler will fail loudly if we miss something
	// important.
	return t
}

// typeOnlyFieldList mirrors the rewriter's helper of the same name:
// formats a field list with types only, comma-separated, repeating the
// type once per name.
func typeOnlyFieldList(fset *token.FileSet, fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fields.List {
		typStr := nodeToString(fset, f.Type)
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			parts = append(parts, typStr)
		}
	}
	return strings.Join(parts, ", ")
}
