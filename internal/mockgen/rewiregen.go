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

// InterfaceResolver resolves an embedded interface reference to its
// source bytes. Given the import path of the package that declares the
// interface and its bare name, it returns the raw bytes of the .go file
// in that package that contains the interface declaration.
//
// Used by GenerateRewireMock to walk embedded interface chains — both
// same-package (other files) and cross-package (e.g. io.Reader). The
// mockgen package deliberately does no filesystem I/O itself; all
// package/file lookup happens in the resolver the toolexec wrapper
// supplies.
//
// May be nil when no embed resolution is needed (non-embedding
// interfaces, or when the only embeds are in the current file). If a
// cross-file embed is encountered and the resolver is nil,
// GenerateRewireMock returns a clear error.
type InterfaceResolver func(importPath, interfaceName string) ([]byte, error)

// flatMethod is an interface method flattened with any type-parameter
// substitutions already applied. Own methods and promoted (embedded)
// methods become flatMethod values in a single flat list.
//
// Each flatMethod carries its own fset and fileImports because embedded
// methods originate in a different file — possibly a different package
// — than the root interface. nodeToString uses the fset; the caller
// resolves package-selector imports via fileImports.
type flatMethod struct {
	name        string
	funcType    *ast.FuncType     // post-substitution
	fset        *token.FileSet    // fset owning funcType's positions
	fileImports map[string]string // local name → import path from the originating file
}

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
//     Go source expression as the user wrote it in the test file.
//   - typeArgImports: local-name → import-path map for any package
//     selectors that appear in the type-arg expressions, derived from
//     the test file's own imports.
//   - resolver: used to fetch source for embedded interfaces that are
//     not declared in `src`. Nil is acceptable when the interface has
//     no embeds (or only same-file embeds).
func GenerateRewireMock(
	src []byte,
	interfaceName, interfacePkgPath, interfacePkgAlias, outputPkg string,
	typeArgs []string,
	typeArgImports map[string]string,
	resolver InterfaceResolver,
) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	iface, ifaceTypeParams, ok := findInterface(file, interfaceName)
	if !ok {
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

	// Parse each typeArg source string into an ast.Expr once, at the
	// root level. These parsed expressions flow through the recursive
	// embed walker so nested substitutions see concrete AST nodes.
	rootTypeArgExprs := make([]ast.Expr, len(typeArgs))
	for i, s := range typeArgs {
		expr, parseErr := parser.ParseExpr(s)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing type argument %q for interface %q: %w", s, interfaceName, parseErr)
		}
		rootTypeArgExprs[i] = expr
	}

	visited := map[string]bool{
		interfacePkgPath + "." + interfaceName: true,
	}
	flat, err := collectFlatMethods(
		iface, ifaceTypeParams, rootTypeArgExprs,
		fset, file,
		interfacePkgPath,
		resolver, visited,
	)
	if err != nil {
		return nil, fmt.Errorf("collecting methods for interface %q: %w", interfaceName, err)
	}

	// Dedupe by method name. Go requires interfaces with overlapping
	// embeds to declare identical signatures, so taking the first
	// occurrence is equivalent to taking either — matches the way Go
	// itself resolves promoted methods.
	{
		seen := map[string]bool{}
		out := flat[:0]
		for _, m := range flat {
			if seen[m.name] {
				continue
			}
			seen[m.name] = true
			out = append(out, m)
		}
		flat = out
	}

	type method struct {
		name         string
		params       string // "name string, age int"
		results      string // "(string, error)" or "string" or ""
		namedResults string // "(_r0 string)" — for zero-return bare-return style
		paramNames   string // "name, age"
		hasResults   bool
		mockFnType   string // user-replacement func type: "func(bar.GreeterIface, string) string"
		pkgRefs      map[string]bool
		fileImports  map[string]string
	}

	var methods []method

	for _, fm := range flat {
		isVariadic := isVariadicFunc(fm.funcType)
		params := ensureParamNames(fm.funcType.Params)

		paramsSrc := fieldListToString(fm.fset, params)
		paramNamesSrc := buildCallArgs(params, isVariadic)
		hasResults := fm.funcType.Results != nil && len(fm.funcType.Results.List) > 0
		resultsSrc := ""
		namedResultsSrc := ""
		if hasResults {
			resultsSrc = resultsToString(fm.fset, fm.funcType.Results)
			namedResultsSrc = addResultNames(fm.fset, resultsSrc)
		}

		// Track packages referenced in the (post-substitution) method
		// signature. These come from the method's originating file —
		// for own methods that's the root file, for promoted methods
		// that's the embed's file.
		pkgRefs := map[string]bool{}
		collectPkgRefs(fm.funcType, pkgRefs)

		// The replacement function type seen from the test author's
		// perspective: receiver is the interface type (with alias and
		// instantiated type args, if any), followed by the method's
		// parameters (types only). Receiver always uses the ROOT
		// interface's alias — promoted methods are still bound to the
		// outer type in Go's view.
		replRecv := interfacePkgAlias + "." + interfaceName
		if len(typeArgs) > 0 {
			replRecv += "[" + strings.Join(typeArgs, ", ") + "]"
		}
		paramTypesOnly := typeOnlyFieldList(fm.fset, params)
		mockFnParams := replRecv
		if paramTypesOnly != "" {
			mockFnParams += ", " + paramTypesOnly
		}
		mockFnType := "func(" + mockFnParams + ")"
		if resultsSrc != "" {
			mockFnType += " " + resultsSrc
		}

		methods = append(methods, method{
			name:         fm.name,
			params:       paramsSrc,
			results:      resultsSrc,
			namedResults: namedResultsSrc,
			paramNames:   paramNamesSrc,
			hasResults:   hasResults,
			mockFnType:   mockFnType,
			pkgRefs:      pkgRefs,
			fileImports:  fm.fileImports,
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

	// Imports — deduplicated by (alias, path) to ensure repeated
	// addImport calls don't emit the same line twice. We track both
	// the alias (so we don't import two packages under the same local
	// name) and the import path (so the same package isn't imported
	// twice under different aliases).
	var usedImports []string
	importedAliases := map[string]bool{}
	importedPaths := map[string]bool{}
	addImport := func(path, alias string) {
		if importedPaths[path] {
			return
		}
		effectiveAlias := alias
		if effectiveAlias == "" {
			effectiveAlias = defaultPkgAlias(path)
		}
		if importedAliases[effectiveAlias] {
			return
		}
		importedAliases[effectiveAlias] = true
		importedPaths[path] = true
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

	// Resolve imports referenced by each method's signature. Per-method
	// resolution order:
	//
	//   1. typeArgImports — packages referenced by the test file's
	//      type-arg expressions (e.g. "time" → "time" when the user
	//      wrote ContainerIface[time.Duration]). Applies uniformly to
	//      substituted identifiers in any method, own or promoted.
	//
	//   2. method.fileImports — the originating file's imports. For own
	//      methods that's the root file; for promoted methods it's the
	//      embed's file. Covers package selectors that were already
	//      present in the method signature before substitution (e.g.
	//      context.Context in io.ReaderAt).
	for _, m := range methods {
		for localName := range m.pkgRefs {
			if path, ok := typeArgImports[localName]; ok {
				addImport(path, localName)
				continue
			}
			if path, ok := m.fileImports[localName]; ok {
				addImport(path, localName)
			}
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
	// time, so the keys can never drift.
	//
	// For promoted methods, the registry key uses the ROOT interface
	// name, not the embed's interface name. That matches what
	// runtime.FuncForPC reports for method expressions on the outer
	// interface: `pkg.Outer.Method`, even when Method is promoted from
	// an embed like io.Reader. Users stub as bar.Outer.Read, and that
	// resolves to pkg.Outer.Read at runtime.
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

// collectFlatMethods walks iface's own methods and any embedded
// interfaces (recursively) and returns a flat list of methods with the
// current substitution applied.
//
//   - typeArgs is the already-substituted list of ast.Exprs
//     representing the instantiation of iface's type parameters as
//     seen from the root caller's perspective.
//   - fset + file describe the source iface was parsed from; used for
//     printing and for this file's imports.
//   - pkgPath is iface's declaring package import path; used for
//     visited-set keys and for resolving same-package embeds.
//   - resolver is used to fetch source for embeds that aren't declared
//     in `file`. May be nil; cross-file embeds error clearly when nil.
//   - visited guards against cycles (Go rejects embed cycles, but
//     defensive anyway).
func collectFlatMethods(
	iface *ast.InterfaceType,
	typeParams *ast.FieldList,
	typeArgs []ast.Expr,
	fset *token.FileSet,
	file *ast.File,
	pkgPath string,
	resolver InterfaceResolver,
	visited map[string]bool,
) ([]flatMethod, error) {
	// Build the type-parameter substitution for this level: T → <expr>,
	// K → <expr>, etc. For non-generic interfaces the subst is empty
	// and substituteFuncType is a no-op.
	subst := map[string]ast.Expr{}
	if typeParams != nil && len(typeArgs) > 0 {
		idx := 0
		for _, field := range typeParams.List {
			for _, name := range field.Names {
				if idx >= len(typeArgs) {
					return nil, fmt.Errorf("type-argument arity mismatch: interface declares %d type parameters but received %d arguments",
						typeParams.NumFields(), len(typeArgs))
				}
				subst[name.Name] = typeArgs[idx]
				idx++
			}
		}
	}

	fileImports := buildFileImports(file)

	var out []flatMethod

	for _, field := range iface.Methods.List {
		// Own method: named field with a function-type body.
		if len(field.Names) > 0 {
			funcType, ok := field.Type.(*ast.FuncType)
			if !ok {
				return nil, fmt.Errorf("unexpected non-function interface field %q", field.Names[0].Name)
			}
			out = append(out, flatMethod{
				name:        field.Names[0].Name,
				funcType:    substituteFuncType(funcType, subst),
				fset:        fset,
				fileImports: fileImports,
			})
			continue
		}

		// Embedded interface: names is empty, type is a reference.
		embed, err := parseEmbedRef(field.Type)
		if err != nil {
			return nil, err
		}

		// Resolve embed's declaring package. If it's a same-package
		// reference, we look first in our already-parsed file, then
		// (if not found there) fall back to the resolver for other
		// files in the same package.
		embedPkgPath := embed.pkgPath
		if embedPkgPath == "" {
			embedPkgPath = pkgPath
		} else {
			// Cross-package reference: the embed.pkgPath we parsed is
			// actually just a local alias (like "io" in "io.Reader").
			// Resolve it to an import path via the current file's
			// imports.
			path, ok := fileImports[embed.pkgPath]
			if !ok {
				return nil, fmt.Errorf("embedded interface %s.%s references package alias %q that is not imported in the declaring file",
					embed.pkgPath, embed.ifaceName, embed.pkgPath)
			}
			embedPkgPath = path
		}

		visitKey := embedPkgPath + "." + embed.ifaceName
		if visited[visitKey] {
			continue
		}

		// Apply the current level's substitution to the embed's type
		// arguments before recursing. For Outer[U] embedding Base[U],
		// with U → int, Base gets called with type args [int].
		substitutedEmbedArgs := make([]ast.Expr, len(embed.typeArgExprs))
		for i, expr := range embed.typeArgExprs {
			substitutedEmbedArgs[i] = substTypeExpr(expr, subst)
		}

		// Locate the embed's AST: either already in our file (same
		// package, same file) or fetched via resolver.
		var (
			embedFset  *token.FileSet
			embedFile  *ast.File
			embedIface *ast.InterfaceType
			embedTP    *ast.FieldList
		)
		if embed.pkgPath == "" {
			// Same package. Try this file first.
			if ifType, tp, ok := findInterface(file, embed.ifaceName); ok {
				embedFset = fset
				embedFile = file
				embedIface = ifType
				embedTP = tp
			}
		}

		if embedIface == nil {
			if resolver == nil {
				return nil, fmt.Errorf("embedded interface %s.%s: cannot resolve without an InterfaceResolver (needed for cross-file or cross-package embeds)",
					embedPkgPath, embed.ifaceName)
			}
			src, err := resolver(embedPkgPath, embed.ifaceName)
			if err != nil {
				return nil, fmt.Errorf("resolving embedded interface %s.%s: %w", embedPkgPath, embed.ifaceName, err)
			}
			efs := token.NewFileSet()
			ef, err := parser.ParseFile(efs, "", src, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("parsing embedded interface source for %s.%s: %w", embedPkgPath, embed.ifaceName, err)
			}
			ifType, tp, ok := findInterface(ef, embed.ifaceName)
			if !ok {
				return nil, fmt.Errorf("embedded interface %s.%s not found in resolved source", embedPkgPath, embed.ifaceName)
			}
			embedFset = efs
			embedFile = ef
			embedIface = ifType
			embedTP = tp
		}

		// Mark visited before the recursive call so mutually-embedding
		// interfaces can't spin forever (Go forbids this but defense in
		// depth is cheap).
		visited[visitKey] = true

		nested, err := collectFlatMethods(
			embedIface, embedTP, substitutedEmbedArgs,
			embedFset, embedFile,
			embedPkgPath, resolver, visited,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, nested...)
	}

	return out, nil
}

// findInterface locates a top-level interface declaration by name in
// file. Returns the InterfaceType, its TypeParams (nil for non-generic),
// and whether it was found.
func findInterface(file *ast.File, name string) (*ast.InterfaceType, *ast.FieldList, bool) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				continue
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				return nil, nil, false
			}
			return iface, ts.TypeParams, true
		}
	}
	return nil, nil, false
}

// buildFileImports returns the map of local-name → import-path for a
// parsed file.
func buildFileImports(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		var localName string
		if imp.Name != nil {
			localName = imp.Name.Name
		} else {
			localName = defaultPkgAlias(importPath)
		}
		out[localName] = importPath
	}
	return out
}

// embedRef describes a single embedded-interface reference parsed from
// an ast.Field.Type expression.
//
//	pkgPath == ""       same-package embed (Reader, Base[T])
//	pkgPath == "io"     cross-package embed (io.Reader, pkg.Base[T])
//	                    — value is the LOCAL alias, not the import path
//	ifaceName           the embedded interface's bare name
//	typeArgExprs        parsed type-arg expressions (empty for non-generic)
type embedRef struct {
	pkgPath      string
	ifaceName    string
	typeArgExprs []ast.Expr
}

// parseEmbedRef decodes an embedded-interface reference expression.
// Supports all forms Go allows in an interface embed position:
//
//	Reader                  → ident
//	io.Reader               → selector
//	Base[T]                 → index (same pkg, 1 type arg)
//	io.Base[T]              → index (cross pkg, 1 type arg)
//	Base[K, V]              → indexList (same pkg, multi type args)
//	io.Base[K, V]           → indexList (cross pkg, multi type args)
func parseEmbedRef(expr ast.Expr) (embedRef, error) {
	switch e := expr.(type) {
	case *ast.Ident:
		return embedRef{ifaceName: e.Name}, nil
	case *ast.SelectorExpr:
		pkgIdent, ok := e.X.(*ast.Ident)
		if !ok {
			return embedRef{}, fmt.Errorf("unsupported embedded selector form: %T", e.X)
		}
		return embedRef{pkgPath: pkgIdent.Name, ifaceName: e.Sel.Name}, nil
	case *ast.IndexExpr:
		base, err := parseEmbedRef(e.X)
		if err != nil {
			return embedRef{}, err
		}
		base.typeArgExprs = []ast.Expr{e.Index}
		return base, nil
	case *ast.IndexListExpr:
		base, err := parseEmbedRef(e.X)
		if err != nil {
			return embedRef{}, err
		}
		base.typeArgExprs = append([]ast.Expr(nil), e.Indices...)
		return base, nil
	}
	return embedRef{}, fmt.Errorf("unsupported embedded-interface form: %T", expr)
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
// can collide if they share the same mangled form. If they do, the
// generated file fails to compile because two structs with the same
// name would be declared, which surfaces the problem clearly.
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
