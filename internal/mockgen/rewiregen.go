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

// PackageTypeLister returns the set of exported top-level type names
// declared in the package at importPath. Used to support dot imports
// (`import . "pkg"`): when an interface's declaring file pulls a
// package into its top-level scope via a dot import, any bare
// identifier in a method signature that matches a name in that pkg
// must be qualified with the dot-imported package's alias rather than
// the interface's own package alias.
//
// Implementation lives in the toolexec wrapper (readdir + ast parse).
// mockgen calls the lister only when it detects `.` imports in a
// parsed interface file. nil is fine for interfaces that have no dot
// imports; if a dot import is present and the lister is nil, mockgen
// returns a clear error.
type PackageTypeLister func(importPath string) (map[string]bool, error)

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

	// originPkgPath is the import path of the package that declared
	// this method. Used to import the origin package in the generated
	// file when the method's signature references types from its own
	// package as bare identifiers.
	originPkgPath string
	// originPkgAlias is the alias to use when qualifying bare
	// same-package type references in this method's signature. For
	// methods originating in the root interface's package this is the
	// caller-supplied interfacePkgAlias; for methods from embedded
	// interfaces in other packages it defaults to the origin package's
	// last-segment name.
	originPkgAlias string

	// dotImportAliasToPath maps a local alias chosen for a dot-imported
	// package (in this method's originating file) to its import path.
	// Populated only when the originating file has `.` imports. Used
	// at render time to emit the right `import "pkg"` lines for any
	// aliases that end up referenced in the qualified funcType.
	dotImportAliasToPath map[string]string
}

// dotImportInfo describes the set of dot-imported packages active in
// a single parsed file. nameToAlias maps an exported type name
// brought in by a `.` import to the alias we'll use when qualifying
// it; aliasToPath maps that alias to the real import path so the
// render step can emit the `import "path"` line.
//
// aliases are always the dot-imported package's default last-segment
// name (e.g. "io" for `import . "io"`) to avoid clashes with the
// file's other imports.
type dotImportInfo struct {
	nameToAlias map[string]string // typeName → alias
	aliasToPath map[string]string // alias → import path
}

// empty reports whether the level has no dot-imported types.
func (d *dotImportInfo) empty() bool {
	return d == nil || len(d.nameToAlias) == 0
}

// GenerateRewireMock produces a Go source file for the test package
// `outputPkg` that declares a concrete backing struct implementing the
// interface `interfaceName` (resolved from `src`). The generated
// backing struct uses per-instance dispatch via package-level
// sync.Maps and registers itself with the rewire runtime via init()
// so that rewire.NewMock[I] and rewire.InstanceFunc work on it.
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
//   - typeLister: used to list exported type names in a package
//     referenced via `import . "pkg"`. Nil is acceptable when no dot
//     imports are present; if a `.` import is encountered and the
//     lister is nil, generation fails with a clear error.
func GenerateRewireMock(
	src []byte,
	interfaceName, interfacePkgPath, interfacePkgAlias, outputPkg string,
	typeArgs []string,
	typeArgImports map[string]string,
	resolver InterfaceResolver,
	typeLister PackageTypeLister,
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
		interfacePkgPath, interfacePkgAlias,
		resolver, typeLister, visited,
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

	// originPkgImports collects (originPkgPath → originPkgAlias) for
	// packages whose aliases actually appear in a method signature —
	// i.e. where qualifyBareTypes rewrote at least one bare Ident to
	// `alias.Ident`. Populated in the loop below and used to emit
	// exactly the right set of origin imports.
	originPkgImports := map[string]string{}

	var methods []method

	for _, fm := range flat {
		// flatMethod.funcType is already qualified + substituted by
		// collectFlatMethods. No further transformation needed here —
		// just print it.
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

		// Track packages referenced in the final method signature.
		pkgRefs := map[string]bool{}
		collectPkgRefs(fm.funcType, pkgRefs)

		// Record the origin package for import emission, but only if
		// qualifyBareTypes actually rewrote bare idents to reference
		// the origin alias. Without this check we'd emit unused
		// imports for embed methods with signatures that don't touch
		// any same-package types (e.g. io.Reader.Read → only builtins,
		// so we don't need to import "io" in the generated file).
		if fm.originPkgPath != "" && fm.originPkgAlias != "" && pkgRefs[fm.originPkgAlias] {
			originPkgImports[fm.originPkgPath] = fm.originPkgAlias
		}

		// Similarly record any dot-import aliases that the qualifier
		// actually used. These come from `.` imports in the method's
		// originating file — e.g. the file did `import . "io"`, the
		// qualifier rewrote bare `Reader` to `io.Reader`, and now we
		// need to import "io" in the generated mock.
		for alias, path := range fm.dotImportAliasToPath {
			if pkgRefs[alias] {
				originPkgImports[path] = alias
			}
		}

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

	// Emit an import for every origin package we saw during method
	// flattening — qualifyBareTypes rewrote bare same-package type refs
	// to `alias.Ident` form, so the alias must be in scope. Root
	// methods share interfacePkgPath / interfacePkgAlias (already
	// added); embed methods from other packages bring their own.
	for path, alias := range originPkgImports {
		addImport(path, alias)
	}

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
//   - pkgAlias is the local alias used to qualify bare same-package
//     type references in methods originating at this level. At the
//     root it's the caller-supplied interfacePkgAlias; for recursive
//     cross-package embed calls it's the embed package's default
//     alias (last path segment).
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
	pkgAlias string,
	resolver InterfaceResolver,
	typeLister PackageTypeLister,
	visited map[string]bool,
) ([]flatMethod, error) {
	// Build the type-parameter substitution for this level: T → <expr>,
	// K → <expr>, etc. For non-generic interfaces the subst is empty
	// and substituteFuncType is a no-op. We also build a typeParamNames
	// set used as the skip set for qualification — we must NOT qualify
	// type params with the pkg alias (they're about to be substituted).
	subst := map[string]ast.Expr{}
	typeParamNames := map[string]bool{}
	if typeParams != nil {
		idx := 0
		for _, field := range typeParams.List {
			for _, name := range field.Names {
				typeParamNames[name.Name] = true
				if idx < len(typeArgs) {
					subst[name.Name] = typeArgs[idx]
				}
				idx++
			}
		}
		if len(typeArgs) > 0 && idx != len(typeArgs) {
			return nil, fmt.Errorf("type-argument arity mismatch: interface declares %d type parameters but received %d arguments",
				idx, len(typeArgs))
		}
	}

	fileImports := buildFileImports(file)

	// Detect `import . "pkg"` entries and, for each, build the
	// nameToAlias / aliasToPath maps by asking the type lister for the
	// dot-imported package's exported types. This information is used
	// by qualifyBareTypes (to rewrite `Foo` → `io.Foo` instead of
	// `declaringPkg.Foo`) and by embed resolution (to treat a bare
	// ident embed as cross-package when the name is dot-imported).
	dotImports, err := buildDotImportInfo(file, pkgAlias, typeLister)
	if err != nil {
		return nil, err
	}

	var out []flatMethod

	for _, field := range iface.Methods.List {
		// Own method: named field with a function-type body.
		if len(field.Names) > 0 {
			funcType, ok := field.Type.(*ast.FuncType)
			if !ok {
				return nil, fmt.Errorf("unexpected non-function interface field %q", field.Names[0].Name)
			}
			// Qualify bare type refs FIRST (skipping this level's type
			// params), THEN substitute. Qualification checks
			// dot-imported names first, then falls back to same-pkg
			// qualification. Substituted-in exprs come from the
			// parent's scope and are already qualified from the
			// parent's perspective.
			qualified := qualifyFuncType(funcType, pkgAlias, typeParamNames, dotImports)
			substituted := substituteFuncType(qualified, subst)
			fm := flatMethod{
				name:           field.Names[0].Name,
				funcType:       substituted,
				fset:           fset,
				fileImports:    fileImports,
				originPkgPath:  pkgPath,
				originPkgAlias: pkgAlias,
			}
			if !dotImports.empty() {
				fm.dotImportAliasToPath = dotImports.aliasToPath
			}
			out = append(out, fm)
			continue
		}

		// Embedded interface: names is empty, type is a reference.
		embed, err := parseEmbedRef(field.Type)
		if err != nil {
			return nil, err
		}

		// If a bare-ident embed's name matches a dot-imported
		// type, the embed is actually cross-package. Rewrite the
		// embedRef so downstream resolution follows the dot-imported
		// path instead of assuming same-package.
		if embed.pkgPath == "" && !dotImports.empty() {
			if alias, ok := dotImports.nameToAlias[embed.ifaceName]; ok {
				embed.pkgPath = alias
			}
		}

		// Resolve embed's declaring package. If it's a same-package
		// reference, we look first in our already-parsed file, then
		// (if not found there) fall back to the resolver for other
		// files in the same package.
		embedPkgPath := embed.pkgPath
		if embedPkgPath == "" {
			embedPkgPath = pkgPath
		} else if dotPath, ok := dotImports.aliasToPath[embed.pkgPath]; ok {
			// Alias was picked up from a dot-imported package just
			// above — resolve to the real import path.
			embedPkgPath = dotPath
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

		// Qualify the embed's type args against THIS level's pkg
		// alias (skipping this level's type params), then apply this
		// level's substitution, before recursing. That way any
		// same-pkg bare idents in the embed's type-arg list (e.g.
		// `pkgA.Inner[*Widget]` where Widget lives in the current
		// package) become properly qualified before they enter the
		// child's scope. The child level qualifies its OWN method
		// signatures with its own alias; substituted-in exprs stay
		// as-is because qualifyBareTypes leaves SelectorExpr alone.
		substitutedEmbedArgs := make([]ast.Expr, len(embed.typeArgExprs))
		for i, expr := range embed.typeArgExprs {
			q := qualifyBareTypes(expr, pkgAlias, typeParamNames, dotImports)
			substitutedEmbedArgs[i] = substTypeExpr(q, subst)
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

		// The recursive call's pkgAlias is used to qualify bare
		// same-package types in the embed's method signatures. For
		// same-package embeds we reuse the current level's alias; for
		// cross-package embeds we prefer the embed file's actual
		// `package X` declaration (which we've already parsed into
		// embedFile), falling back to the default last-segment alias if
		// for some reason the parsed name is missing.
		embedPkgAlias := pkgAlias
		if embedPkgPath != pkgPath {
			if embedFile != nil && embedFile.Name != nil && embedFile.Name.Name != "" {
				embedPkgAlias = embedFile.Name.Name
			} else {
				embedPkgAlias = defaultPkgAlias(embedPkgPath)
			}
		}

		nested, err := collectFlatMethods(
			embedIface, embedTP, substitutedEmbedArgs,
			embedFset, embedFile,
			embedPkgPath, embedPkgAlias,
			resolver, typeLister, visited,
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

// predeclaredTypes is the set of identifiers Go treats as built-in
// types in expression positions. qualifyBareTypes leaves these alone;
// anything else in an Ident position is assumed to be a same-package
// type that needs qualification with the declaring package's alias.
//
// Non-type predeclared names (nil, true, false, iota, append, len, ...)
// are excluded because they can't appear in a type expression.
var predeclaredTypes = map[string]bool{
	"bool":       true,
	"byte":       true,
	"complex64":  true,
	"complex128": true,
	"error":      true,
	"float32":    true,
	"float64":    true,
	"int":        true,
	"int8":       true,
	"int16":      true,
	"int32":      true,
	"int64":      true,
	"rune":       true,
	"string":     true,
	"uint":       true,
	"uint8":      true,
	"uint16":     true,
	"uint32":     true,
	"uint64":     true,
	"uintptr":    true,
	"any":        true,
	"comparable": true,
}

// qualifyBareTypes walks a type expression and wraps any bare
// *ast.Ident referring to a non-predeclared, non-type-parameter type
// with `pkgAlias.Ident`, producing a qualified selector. Idents already
// wrapped in a selector (pkg.Type) are left alone. The result is a new
// tree; the input is not mutated.
//
// This is the core of same-package type qualification: an interface
// declared in package bar/ can write `func() *Greeter` using the bare
// identifier because it's in its own package, but the generated mock
// lives in the test package and must reference the same type as
// `bar.Greeter`. qualifyBareTypes produces the `bar.` prefix.
//
// Must be called BEFORE type-parameter substitution. The skipIdents
// set should contain the names of this level's type parameters so
// they stay bare for the substitution pass to replace.
//
// Safety: Go's type expression grammar only admits idents that are
// (a) predeclared type names, (b) type parameters in scope, or
// (c) types declared in the current package. Cases (a) and (b) are
// excluded via predeclaredTypes and skipIdents; anything left is (c).
// Dot imports (`import . "pkg"`) are the one exception — rare and
// discouraged, we accept imperfect handling there.
func qualifyBareTypes(t ast.Expr, pkgAlias string, skipIdents map[string]bool, dotImports *dotImportInfo) ast.Expr {
	if t == nil {
		return t
	}
	switch e := t.(type) {
	case *ast.Ident:
		// Dot-imported names take priority over same-pkg qualification:
		// the file says `import . "io"`, so bare `Reader` means
		// `io.Reader`, not `declaringpkg.Reader`.
		if !dotImports.empty() {
			if alias, ok := dotImports.nameToAlias[e.Name]; ok {
				return &ast.SelectorExpr{
					X:   ast.NewIdent(alias),
					Sel: ast.NewIdent(e.Name),
				}
			}
		}
		if pkgAlias == "" || predeclaredTypes[e.Name] || e.Name == "_" || skipIdents[e.Name] {
			return e
		}
		return &ast.SelectorExpr{
			X:   ast.NewIdent(pkgAlias),
			Sel: ast.NewIdent(e.Name),
		}
	case *ast.StarExpr:
		return &ast.StarExpr{Star: e.Star, X: qualifyBareTypes(e.X, pkgAlias, skipIdents, dotImports)}
	case *ast.ArrayType:
		return &ast.ArrayType{Lbrack: e.Lbrack, Len: e.Len, Elt: qualifyBareTypes(e.Elt, pkgAlias, skipIdents, dotImports)}
	case *ast.MapType:
		return &ast.MapType{
			Map:   e.Map,
			Key:   qualifyBareTypes(e.Key, pkgAlias, skipIdents, dotImports),
			Value: qualifyBareTypes(e.Value, pkgAlias, skipIdents, dotImports),
		}
	case *ast.ChanType:
		return &ast.ChanType{
			Begin: e.Begin,
			Arrow: e.Arrow,
			Dir:   e.Dir,
			Value: qualifyBareTypes(e.Value, pkgAlias, skipIdents, dotImports),
		}
	case *ast.FuncType:
		return &ast.FuncType{
			Func:    e.Func,
			Params:  qualifyBareTypesInFieldList(e.Params, pkgAlias, skipIdents, dotImports),
			Results: qualifyBareTypesInFieldList(e.Results, pkgAlias, skipIdents, dotImports),
		}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Ellipsis: e.Ellipsis, Elt: qualifyBareTypes(e.Elt, pkgAlias, skipIdents, dotImports)}
	case *ast.IndexExpr:
		return &ast.IndexExpr{
			X:      qualifyBareTypes(e.X, pkgAlias, skipIdents, dotImports),
			Lbrack: e.Lbrack,
			Index:  qualifyBareTypes(e.Index, pkgAlias, skipIdents, dotImports),
			Rbrack: e.Rbrack,
		}
	case *ast.IndexListExpr:
		newIndices := make([]ast.Expr, len(e.Indices))
		for i, ix := range e.Indices {
			newIndices[i] = qualifyBareTypes(ix, pkgAlias, skipIdents, dotImports)
		}
		return &ast.IndexListExpr{
			X:       qualifyBareTypes(e.X, pkgAlias, skipIdents, dotImports),
			Lbrack:  e.Lbrack,
			Indices: newIndices,
			Rbrack:  e.Rbrack,
		}
	case *ast.SelectorExpr:
		// Already qualified — leave the selector alone. Don't recurse
		// into e.X: in a type-expression position it must be a package
		// identifier, which has no bare-type semantics.
		return e
	case *ast.InterfaceType:
		return &ast.InterfaceType{
			Interface:  e.Interface,
			Methods:    qualifyBareTypesInFieldList(e.Methods, pkgAlias, skipIdents, dotImports),
			Incomplete: e.Incomplete,
		}
	case *ast.StructType:
		return &ast.StructType{
			Struct:     e.Struct,
			Fields:     qualifyBareTypesInFieldList(e.Fields, pkgAlias, skipIdents, dotImports),
			Incomplete: e.Incomplete,
		}
	}
	return t
}

func qualifyBareTypesInFieldList(fl *ast.FieldList, pkgAlias string, skipIdents map[string]bool, dotImports *dotImportInfo) *ast.FieldList {
	if fl == nil {
		return nil
	}
	newList := make([]*ast.Field, len(fl.List))
	for i, f := range fl.List {
		newField := *f
		newField.Type = qualifyBareTypes(f.Type, pkgAlias, skipIdents, dotImports)
		newList[i] = &newField
	}
	return &ast.FieldList{Opening: fl.Opening, Closing: fl.Closing, List: newList}
}

// qualifyFuncType applies qualifyBareTypes to a function type's params
// and results, returning a new FuncType. Passes through unchanged
// when there's no pkgAlias and no dot imports.
func qualifyFuncType(ft *ast.FuncType, pkgAlias string, skipIdents map[string]bool, dotImports *dotImportInfo) *ast.FuncType {
	if ft == nil {
		return ft
	}
	if pkgAlias == "" && dotImports.empty() {
		return ft
	}
	return &ast.FuncType{
		Func:    ft.Func,
		Params:  qualifyBareTypesInFieldList(ft.Params, pkgAlias, skipIdents, dotImports),
		Results: qualifyBareTypesInFieldList(ft.Results, pkgAlias, skipIdents, dotImports),
	}
}

// buildDotImportInfo scans file.Imports for `.` imports and builds
// the (nameToAlias, aliasToPath) maps needed for dot-import-aware
// qualification. Returns an empty info (with nil maps) when the file
// has no dot imports, in which case qualifyBareTypes skips the
// lookup and falls back to same-pkg qualification.
//
// If the file has a dot import but typeLister is nil,
// buildDotImportInfo fails with a targeted error — we can't resolve
// the symbols without help from the toolexec wrapper.
//
// If two different dot-imported packages would pick the same default
// alias, or if a dot-imported package's default alias collides with
// the interface's own declaring alias, we fail with a clear message.
// Both cases are rare in practice; when they happen the user can
// restructure the interface's declaring file.
func buildDotImportInfo(file *ast.File, declaringPkgAlias string, typeLister PackageTypeLister) (*dotImportInfo, error) {
	var dotImportPaths []string
	for _, imp := range file.Imports {
		if imp.Name != nil && imp.Name.Name == "." {
			dotImportPaths = append(dotImportPaths, strings.Trim(imp.Path.Value, `"`))
		}
	}
	if len(dotImportPaths) == 0 {
		return &dotImportInfo{}, nil
	}
	if typeLister == nil {
		return nil, fmt.Errorf("interface's declaring file uses `.` imports (%v) but no PackageTypeLister was supplied — dot-import-aware mock generation needs the toolexec wrapper's resolver", dotImportPaths)
	}
	info := &dotImportInfo{
		nameToAlias: map[string]string{},
		aliasToPath: map[string]string{},
	}
	for _, path := range dotImportPaths {
		alias := defaultPkgAlias(path)
		if alias == declaringPkgAlias {
			return nil, fmt.Errorf("dot-imported package %q has the same default alias %q as the interface's own package — please rename or restructure", path, alias)
		}
		if existingPath, clash := info.aliasToPath[alias]; clash && existingPath != path {
			return nil, fmt.Errorf("dot-imported packages %q and %q both resolve to alias %q — please rename or restructure", existingPath, path, alias)
		}
		names, err := typeLister(path)
		if err != nil {
			return nil, fmt.Errorf("listing exported types of dot-imported package %q: %w", path, err)
		}
		info.aliasToPath[alias] = path
		for name := range names {
			if _, exists := info.nameToAlias[name]; exists {
				// Same name imported via two different dot-imported
				// packages — Go itself would reject this as
				// ambiguous, so the interface's source wouldn't even
				// compile. Fail with a matching diagnostic.
				return nil, fmt.Errorf("name %q is brought in by two different dot-imported packages — ambiguous", name)
			}
			info.nameToAlias[name] = alias
		}
	}
	return info, nil
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
