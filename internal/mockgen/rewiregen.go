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
// Phase 1 scope: non-generic interface, no embedded interfaces,
// method signatures using only builtin types or types already
// qualified with a package selector. Types from the interface's own
// declaring package (e.g. a method returning a `*Greeter` where
// `Greeter` is in the same package as `GreeterIface`) are a Phase 2
// item.
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
func GenerateRewireMock(src []byte, interfaceName, interfacePkgPath, interfacePkgAlias, outputPkg string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	var iface *ast.InterfaceType
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
			if typeSpec.TypeParams != nil && typeSpec.TypeParams.NumFields() > 0 {
				return nil, fmt.Errorf("generic interfaces are not yet supported (interface %q has type parameters)", interfaceName)
			}
			iface = ifaceType
		}
	}
	if iface == nil {
		return nil, fmt.Errorf("interface %q not found", interfaceName)
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
		params := ensureParamNames(funcType.Params)
		isVariadic := isVariadicFunc(funcType)

		paramsSrc := fieldListToString(fset, params)
		paramNamesSrc := buildCallArgs(params, isVariadic)
		hasResults := funcType.Results != nil && len(funcType.Results.List) > 0
		resultsSrc := ""
		namedResultsSrc := ""
		if hasResults {
			resultsSrc = resultsToString(fset, funcType.Results)
			namedResultsSrc = addResultNames(fset, resultsSrc)
		}

		// Track packages referenced in the method signature so we can
		// emit only the imports we actually need.
		collectPkgRefs(funcType, usedPkgs)

		// The replacement function type, seen from the test author's
		// perspective: receiver is the interface type (with alias),
		// followed by the method's parameters (types only).
		replRecv := interfacePkgAlias + "." + interfaceName
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

	// Derive synthetic names from (interfacePkgAlias, interfaceName).
	// All identifiers use ASCII-only characters compatible with Go syntax.
	structName := "_rewire_mock_" + interfacePkgAlias + "_" + interfaceName

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
	// parameter flows the interface type through to reflect at runtime,
	// which derives the registry key from PkgPath()+"."+Name(). Same
	// derivation NewMock[I] uses at lookup time, so the keys can never
	// drift. The generated file doesn't need to import reflect.
	fullIfaceName := interfacePkgPath + "." + interfaceName
	b.WriteString("func init() {\n")
	fmt.Fprintf(&b, "\trewire.RegisterMockFactory[%s.%s](func() any { return &%s{} })\n",
		interfacePkgAlias, interfaceName, structName)
	for _, m := range methods {
		fmt.Fprintf(&b, "\trewire.RegisterByInstance(%q, &Mock_%s_%s_ByInstance)\n",
			fullIfaceName+"."+m.name, structName, m.name)
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
