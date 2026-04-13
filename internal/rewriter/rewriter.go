package rewriter

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
)

// RewriteSource takes Go source code and a function name, and returns
// modified source where the function is made swappable via a Mock_ variable.
//
// Given:
//
//	func Hello(name string) string { return "hello " + name }
//
// It produces:
//
//	var Mock_Hello func(name string) string
//
//	func Hello(name string) string {
//	    if f := Mock_Hello; f != nil {
//	        return f(name)
//	    }
//	    return _real_Hello(name)
//	}
//
//	func _real_Hello(name string) string { return "hello " + name }
func RewriteSource(src []byte, funcName string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	// Detect method syntax: (*Type).Method or Type.Method
	typeName, methodName, isPointer, isMethod := parseMethodTarget(funcName)

	// Find the target declaration
	var target *ast.FuncDecl
	var targetIdx int
	for i, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if isMethod {
			if fd.Name.Name == methodName && fd.Recv != nil && matchesReceiver(fd.Recv, typeName, isPointer) {
				if fd.Body == nil {
					return nil, fmt.Errorf("method %q has no body (assembly or go:linkname stub)", funcName)
				}
				target = fd
				targetIdx = i
				break
			}
		} else {
			if fd.Name.Name == funcName && fd.Recv == nil {
				if fd.Body == nil {
					return nil, fmt.Errorf("function %q has no body (assembly or go:linkname stub)", funcName)
				}
				target = fd
				targetIdx = i
				break
			}
		}
	}
	if target == nil {
		return nil, fmt.Errorf("function %q not found", funcName)
	}

	// Generic functions take a separate code path that emits a sync.Map-
	// based per-instantiation dispatch table. Generic methods on generic
	// types are not supported in v1.
	if target.Type.TypeParams != nil && target.Type.TypeParams.NumFields() > 0 {
		if isMethod {
			return nil, fmt.Errorf("generic methods are not yet supported (function %q)", funcName)
		}
		return rewriteGenericFunction(src, fset, file, target, funcName)
	}

	// Extract signature info
	params := ensureParamNames(target.Type.Params)
	hasResults := target.Type.Results != nil && len(target.Type.Results.List) > 0
	isVariadic := isVariadicFunc(target.Type)

	// Determine names
	var mockVarName, realFuncName, realVarName, wrapperName string
	if isMethod {
		mockVarName = fmt.Sprintf("Mock_%s_%s", typeName, methodName)
		realFuncName = fmt.Sprintf("_real_%s_%s", typeName, methodName)
		realVarName = fmt.Sprintf("Real_%s_%s", typeName, methodName)
		wrapperName = methodName
	} else {
		mockVarName = "Mock_" + funcName
		realFuncName = "_real_" + funcName
		realVarName = "Real_" + funcName
		wrapperName = funcName
	}

	// Build call args (with variadic spread on last param)
	callArgs := buildCallArgs(params, isVariadic)

	// Mock call args and real call expression — methods prepend receiver
	mockCallArgs := callArgs
	realCallExpr := fmt.Sprintf("%s(%s)", realFuncName, callArgs)
	recvDecl := ""

	if isMethod {
		recvField := target.Recv.List[0]
		recvName := "_rewire_recv"
		if len(recvField.Names) > 0 && recvField.Names[0].Name != "" {
			recvName = recvField.Names[0].Name
		}
		recvTypeStr, err := nodeToString(fset, recvField.Type)
		if err != nil {
			return nil, fmt.Errorf("printing receiver type: %w", err)
		}
		recvDecl = fmt.Sprintf("(%s %s) ", recvName, recvTypeStr)

		if mockCallArgs != "" {
			mockCallArgs = recvName + ", " + mockCallArgs
		} else {
			mockCallArgs = recvName
		}
		realCallExpr = fmt.Sprintf("%s.%s(%s)", recvName, realFuncName, callArgs)
	}

	// Build mock var type string
	var mockVarType string
	if isMethod {
		recvTypeStr, _ := nodeToString(fset, target.Recv.List[0].Type)
		pSrc := typeOnlyFieldList(fset, params)

		mockParams := recvTypeStr
		if pSrc != "" {
			mockParams += ", " + pSrc
		}

		if hasResults {
			rSrc, _ := resultsToString(fset, target.Type.Results)
			mockVarType = fmt.Sprintf("func(%s) %s", mockParams, rSrc)
		} else {
			mockVarType = fmt.Sprintf("func(%s)", mockParams)
		}
	} else {
		funcTypeSrc, err := nodeToString(fset, target.Type)
		if err != nil {
			return nil, fmt.Errorf("printing function type: %w", err)
		}
		mockVarType = funcTypeSrc
	}

	// Build wrapper body
	var mockBody string
	if hasResults {
		mockBody = fmt.Sprintf(`if _rewire_mock := %s; _rewire_mock != nil {
		return _rewire_mock(%s)
	}
	return %s`, mockVarName, mockCallArgs, realCallExpr)
	} else {
		mockBody = fmt.Sprintf(`if _rewire_mock := %s; _rewire_mock != nil {
		_rewire_mock(%s)
		return
	}
	%s`, mockVarName, mockCallArgs, realCallExpr)
	}

	paramsSrc, err := fieldListToString(fset, params)
	if err != nil {
		return nil, fmt.Errorf("printing params: %w", err)
	}
	resultsSrc := ""
	if hasResults {
		resultsSrc, err = resultsToString(fset, target.Type.Results)
		if err != nil {
			return nil, fmt.Errorf("printing results: %w", err)
		}
	}

	// Build the RHS expression for the exported Real_ alias.
	// For plain funcs: just the renamed function identifier.
	// For methods: a method expression (*Type).<_real_name> or Type.<_real_name>.
	var realAliasRHS string
	if isMethod {
		if isPointer {
			realAliasRHS = fmt.Sprintf("(*%s).%s", typeName, realFuncName)
		} else {
			realAliasRHS = fmt.Sprintf("%s.%s", typeName, realFuncName)
		}
	} else {
		realAliasRHS = realFuncName
	}

	// Generate mock var + real alias + wrapper as source text, then parse to AST
	genSrc := fmt.Sprintf(`package %s

var %s %s

var %s = %s

func %s%s(%s) %s {
	%s
}
`, file.Name.Name, mockVarName, mockVarType, realVarName, realAliasRHS, recvDecl, wrapperName, paramsSrc, resultsSrc, mockBody)

	genFset := token.NewFileSet()
	genFile, err := parser.ParseFile(genFset, "", genSrc, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing generated wrapper (this is a bug in rewire):\n%s\nerror: %w", genSrc, err)
	}

	// Rename the original to _real_
	target.Name.Name = realFuncName

	// Replace the original decl with: Mock var + Real alias + wrapper + renamed original
	newDecls := make([]ast.Decl, 0, len(file.Decls)+3)
	newDecls = append(newDecls, file.Decls[:targetIdx]...)
	newDecls = append(newDecls, genFile.Decls[0]) // var Mock_X
	newDecls = append(newDecls, genFile.Decls[1]) // var Real_X = _real_X
	newDecls = append(newDecls, genFile.Decls[2]) // func wrapper
	newDecls = append(newDecls, target)            // func _real_X (original, renamed)
	newDecls = append(newDecls, file.Decls[targetIdx+1:]...)
	file.Decls = newDecls

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("formatting output: %w", err)
	}
	return buf.Bytes(), nil
}

// rewriteGenericFunction handles the generic-function branch of RewriteSource.
// It emits a sync.Map-based per-instantiation dispatch table so that
// mocking bar.Map[int, string] replaces only the [int, string] instantiation,
// while other instantiations keep running the real implementation.
//
// The generated shape for a function `Map[T, U any](...) ...`:
//
//	var Mock_Map sync.Map   // key: type-sig string, value: mock fn (any)
//
//	func Real_Map[T, U any](in []T, f func(T) U) []U {
//	    return _real_Map(in, f)
//	}
//
//	func Map[T, U any](in []T, f func(T) U) []U {
//	    if _rewire_raw, _rewire_ok := Mock_Map.Load(reflect.TypeOf(Map[T, U]).String()); _rewire_ok {
//	        if _rewire_typed, _rewire_ok := _rewire_raw.(func([]T, func(T) U) []U); _rewire_ok {
//	            return _rewire_typed(in, f)
//	        }
//	    }
//	    return _real_Map(in, f)
//	}
//
//	func _real_Map[T, U any](in []T, f func(T) U) []U { /* original body */ }
//
// Unlike the non-generic path this works at the text level: it computes
// the original target function's byte range, substitutes a hand-built
// replacement block in its place, ensures reflect+sync are imported, and
// runs the whole thing through go/format.Source. The AST-splice trick
// used for non-generics breaks down here because we need to inject new
// imports and the fset position juggling gets ugly.
func rewriteGenericFunction(src []byte, fset *token.FileSet, file *ast.File, target *ast.FuncDecl, funcName string) ([]byte, error) {
	params := ensureParamNames(target.Type.Params)
	hasResults := target.Type.Results != nil && len(target.Type.Results.List) > 0
	isVariadic := isVariadicFunc(target.Type)

	mockVarName := "Mock_" + funcName
	realFuncName := "_real_" + funcName
	realAliasName := "Real_" + funcName
	wrapperName := funcName

	// Print "[T, U any]" and "[T, U]" — the constrained form for decls
	// and the bare form for type-argument references.
	typeParamsSrc, err := fieldListToString(fset, target.Type.TypeParams)
	if err != nil {
		return nil, fmt.Errorf("printing type params: %w", err)
	}
	typeParamsDecl := "[" + typeParamsSrc + "]"

	var typeParamNames []string
	for _, field := range target.Type.TypeParams.List {
		for _, n := range field.Names {
			typeParamNames = append(typeParamNames, n.Name)
		}
	}
	typeParamRef := "[" + strings.Join(typeParamNames, ", ") + "]"

	paramsSrc, err := fieldListToString(fset, params)
	if err != nil {
		return nil, fmt.Errorf("printing params: %w", err)
	}
	resultsSrc := ""
	if hasResults {
		resultsSrc, err = resultsToString(fset, target.Type.Results)
		if err != nil {
			return nil, fmt.Errorf("printing results: %w", err)
		}
	}

	callArgs := buildCallArgs(params, isVariadic)

	// The type of the mock function the wrapper expects: the original
	// signature with no type parameters — at dispatch time T, U, ... are
	// concrete, so the mock is a plain function type.
	mockFnType := "func(" + typeOnlyFieldList(fset, params) + ")"
	if hasResults {
		mockFnType = "func(" + typeOnlyFieldList(fset, params) + ") " + resultsSrc
	}

	var wrapperBody, realAliasBody string
	if hasResults {
		wrapperBody = fmt.Sprintf(`if _rewire_raw, _rewire_ok := %s.Load(reflect.TypeOf(%s%s).String()); _rewire_ok {
		if _rewire_typed, _rewire_ok := _rewire_raw.(%s); _rewire_ok {
			return _rewire_typed(%s)
		}
	}
	return %s(%s)`, mockVarName, wrapperName, typeParamRef, mockFnType, callArgs, realFuncName, callArgs)
		realAliasBody = fmt.Sprintf("return %s(%s)", realFuncName, callArgs)
	} else {
		wrapperBody = fmt.Sprintf(`if _rewire_raw, _rewire_ok := %s.Load(reflect.TypeOf(%s%s).String()); _rewire_ok {
		if _rewire_typed, _rewire_ok := _rewire_raw.(%s); _rewire_ok {
			_rewire_typed(%s)
			return
		}
	}
	%s(%s)`, mockVarName, wrapperName, typeParamRef, mockFnType, callArgs, realFuncName, callArgs)
		realAliasBody = fmt.Sprintf("%s(%s)", realFuncName, callArgs)
	}

	// Print the original function body so we can re-emit it under the
	// renamed _real_ function.
	origBodySrc, err := nodeToString(fset, target.Body)
	if err != nil {
		return nil, fmt.Errorf("printing original body: %w", err)
	}

	// The full replacement block that will substitute for the original
	// target function in the source. No package line, no imports — those
	// live in the rest of the file.
	replacement := fmt.Sprintf(`var %s sync.Map

func %s%s(%s) %s {
	%s
}

func %s%s(%s) %s {
	%s
}

func %s%s(%s) %s %s`,
		mockVarName,
		realAliasName, typeParamsDecl, paramsSrc, resultsSrc, realAliasBody,
		wrapperName, typeParamsDecl, paramsSrc, resultsSrc, wrapperBody,
		realFuncName, typeParamsDecl, paramsSrc, resultsSrc, origBodySrc,
	)

	// Byte offsets of the original target function in src.
	startOff := fset.Position(target.Pos()).Offset
	endOff := fset.Position(target.End()).Offset
	if startOff < 0 || endOff > len(src) || startOff >= endOff {
		return nil, fmt.Errorf("internal error: invalid offsets for target %q: [%d:%d)", funcName, startOff, endOff)
	}

	var buf bytes.Buffer
	buf.Write(src[:startOff])
	buf.WriteString(replacement)
	buf.Write(src[endOff:])
	result := buf.Bytes()

	// Add reflect+sync imports if not already present.
	result, err = ensureImportsText(result, "reflect", "sync")
	if err != nil {
		return nil, fmt.Errorf("ensuring imports: %w", err)
	}

	formatted, err := format.Source(result)
	if err != nil {
		return nil, fmt.Errorf("formatting generic rewrite output: %w\n---\n%s", err, result)
	}
	return formatted, nil
}

// ensureImportsText adds any of the given import paths that aren't already
// imported by src. It operates on source text because the rest of the
// generic rewrite path is also text-based — trying to mix AST mutation
// with text splicing gets tangled.
func ensureImportsText(src []byte, pkgs ...string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing for import check: %w", err)
	}

	existing := map[string]bool{}
	for _, imp := range file.Imports {
		existing[strings.Trim(imp.Path.Value, `"`)] = true
	}

	var toAdd []string
	for _, p := range pkgs {
		if !existing[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return src, nil
	}

	// Find an existing import decl we can extend, or plan a new one.
	var importDecl *ast.GenDecl
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			importDecl = gen
			break
		}
	}

	var insertText strings.Builder
	var insertOff int

	if importDecl != nil {
		// Add new specs before the closing paren (or convert single-line
		// form to block form).
		endOff := fset.Position(importDecl.End()).Offset
		if importDecl.Lparen == token.NoPos {
			// Single-spec form: `import "foo"` — replace entirely with a block.
			startOff := fset.Position(importDecl.Pos()).Offset
			oldSpec, ok := importDecl.Specs[0].(*ast.ImportSpec)
			if !ok {
				return nil, fmt.Errorf("unexpected import spec kind")
			}
			insertText.WriteString("import (\n")
			insertText.WriteString("\t")
			insertText.WriteString(oldSpec.Path.Value)
			insertText.WriteString("\n")
			for _, p := range toAdd {
				fmt.Fprintf(&insertText, "\t%q\n", p)
			}
			insertText.WriteString(")")
			return spliceBytes(src, startOff, endOff, insertText.String()), nil
		}
		// Block form: insert new specs before `)`.
		insertOff = fset.Position(importDecl.Rparen).Offset
		for _, p := range toAdd {
			fmt.Fprintf(&insertText, "\t%q\n", p)
		}
		return spliceBytes(src, insertOff, insertOff, insertText.String()), nil
	}

	// No import decl at all — insert a new one right after the package clause.
	insertOff = fset.Position(file.Name.End()).Offset
	insertText.WriteString("\n\nimport (\n")
	for _, p := range toAdd {
		fmt.Fprintf(&insertText, "\t%q\n", p)
	}
	insertText.WriteString(")")
	return spliceBytes(src, insertOff, insertOff, insertText.String()), nil
}

func spliceBytes(src []byte, start, end int, replacement string) []byte {
	var out bytes.Buffer
	out.Grow(len(src) + len(replacement))
	out.Write(src[:start])
	out.WriteString(replacement)
	out.Write(src[end:])
	return out.Bytes()
}

// RewriteFile reads a file, rewrites the named function, and returns the new source.
func RewriteFile(filePath string, funcName string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing file %s: %w", filePath, err)
	}

	src, err := nodeToBytes(fset, file)
	if err != nil {
		return nil, fmt.Errorf("reading file source: %w", err)
	}

	return RewriteSource(src, funcName)
}

// RewriteAllExported rewrites all exported, non-method, non-generic functions
// in the source. Functions that appear to be already rewritten (have a
// corresponding Mock_ var) are skipped.
func RewriteAllExported(src []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	// Collect names of exported functions eligible for rewriting
	existing := collectDeclNames(file)
	var targets []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fd.Name.Name
		if !ast.IsExported(name) {
			continue
		}
		if fd.Recv != nil {
			continue // skip methods
		}
		if fd.Body == nil {
			continue // skip assembly/linkname stubs
		}
		if fd.Type.TypeParams != nil && fd.Type.TypeParams.NumFields() > 0 {
			continue // skip generic functions
		}
		if existing["Mock_"+name] {
			continue // already rewritten
		}
		if name == "init" || name == "main" {
			continue
		}
		targets = append(targets, name)
	}

	if len(targets) == 0 {
		return src, nil
	}

	// Apply rewrites sequentially (each rewrite changes the source)
	result := src
	for _, name := range targets {
		result, err = RewriteSource(result, name)
		if err != nil {
			return nil, fmt.Errorf("rewriting %s: %w", name, err)
		}
	}
	return result, nil
}

// ListExportedFunctions returns the names of exported functions in the source
// that are eligible for rewriting (same criteria as RewriteAllExported).
func ListExportedFunctions(src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	existing := collectDeclNames(file)
	var names []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fd.Name.Name
		if !ast.IsExported(name) {
			continue
		}
		if fd.Recv != nil {
			continue
		}
		if fd.Type.TypeParams != nil && fd.Type.TypeParams.NumFields() > 0 {
			continue
		}
		if existing["Mock_"+name] {
			continue
		}
		if name == "init" || name == "main" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// collectDeclNames returns the set of top-level declaration names in the file.
func collectDeclNames(file *ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names[d.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names[n.Name] = true
					}
				case *ast.TypeSpec:
					names[s.Name.Name] = true
				}
			}
		}
	}
	return names
}

// ensureParamNames gives synthetic names (p0, p1, ...) to any unnamed parameters.
func ensureParamNames(fields *ast.FieldList) *ast.FieldList {
	if fields == nil {
		return fields
	}
	idx := 0
	result := &ast.FieldList{
		Opening: fields.Opening,
		Closing: fields.Closing,
	}
	for _, f := range fields.List {
		newField := *f
		if len(f.Names) == 0 {
			newField.Names = []*ast.Ident{ast.NewIdent(fmt.Sprintf("p%d", idx))}
			idx++
		} else {
			idx += len(f.Names)
		}
		result.List = append(result.List, &newField)
	}
	return result
}

func paramNames(fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	var names []string
	for _, f := range fields.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

func isVariadicFunc(ft *ast.FuncType) bool {
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return false
	}
	last := ft.Params.List[len(ft.Params.List)-1]
	_, ok := last.Type.(*ast.Ellipsis)
	return ok
}

func nodeToString(fset *token.FileSet, node ast.Node) (string, error) {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func nodeToBytes(fset *token.FileSet, node ast.Node) ([]byte, error) {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fieldListToString(fset *token.FileSet, fields *ast.FieldList) (string, error) {
	if fields == nil || len(fields.List) == 0 {
		return "", nil
	}
	var parts []string
	for _, f := range fields.List {
		typStr, err := nodeToString(fset, f.Type)
		if err != nil {
			return "", err
		}
		if len(f.Names) > 0 {
			var names []string
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" "+typStr)
		} else {
			parts = append(parts, typStr)
		}
	}
	return strings.Join(parts, ", "), nil
}

func resultsToString(fset *token.FileSet, fields *ast.FieldList) (string, error) {
	if fields == nil || len(fields.List) == 0 {
		return "", nil
	}
	s, err := fieldListToString(fset, fields)
	if err != nil {
		return "", err
	}
	if len(fields.List) > 1 || len(fields.List[0].Names) > 0 {
		return "(" + s + ")", nil
	}
	return s, nil
}

// parseMethodTarget detects method syntax in funcName.
// "(*Type).Method" returns (Type, Method, true, true).
// "Type.Method" returns (Type, Method, false, true).
// "Func" returns ("", Func, false, false).
func parseMethodTarget(funcName string) (typeName, methodName string, isPointer, isMethod bool) {
	if strings.HasPrefix(funcName, "(*") {
		if idx := strings.Index(funcName, ")."); idx > 2 {
			return funcName[2:idx], funcName[idx+2:], true, true
		}
	}
	if idx := strings.LastIndex(funcName, "."); idx > 0 {
		return funcName[:idx], funcName[idx+1:], false, true
	}
	return "", funcName, false, false
}

// matchesReceiver checks if a receiver field list matches the expected type and pointer-ness.
func matchesReceiver(recv *ast.FieldList, typeName string, isPointer bool) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	recvType := recv.List[0].Type
	if isPointer {
		starExpr, ok := recvType.(*ast.StarExpr)
		if !ok {
			return false
		}
		ident, ok := starExpr.X.(*ast.Ident)
		return ok && ident.Name == typeName
	}
	ident, ok := recvType.(*ast.Ident)
	return ok && ident.Name == typeName
}

// typeOnlyFieldList formats a field list with only types (no names),
// suitable for use in func type literals where mixing named/unnamed params is invalid.
func typeOnlyFieldList(fset *token.FileSet, fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fields.List {
		typStr, err := nodeToString(fset, f.Type)
		if err != nil {
			continue
		}
		// Each name in the field shares the same type
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

// buildCallArgs constructs the argument list for forwarding calls,
// handling variadic spread on the last parameter.
func buildCallArgs(params *ast.FieldList, isVariadic bool) string {
	names := paramNames(params)
	if isVariadic && len(names) > 0 {
		names[len(names)-1] = names[len(names)-1] + "..."
	}
	return strings.Join(names, ", ")
}
