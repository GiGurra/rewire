package rewriter

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"reflect"
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

	// Function-level type parameters (only legal on plain functions in
	// Go 1.18+ — methods can't declare their own type params).
	if target.Type.TypeParams != nil && target.Type.TypeParams.NumFields() > 0 {
		if isMethod {
			return nil, fmt.Errorf("method-level type parameters are not supported — put them on the receiver type (function %q)", funcName)
		}
		return rewriteGenericFunction(fset, file, target, targetIdx, funcName)
	}

	// Methods on generic types: the method itself has no TypeParams, but
	// its receiver refers to a type whose declaration is generic. Look up
	// the receiver type spec in the file and branch if found.
	if isMethod {
		if typeTypeParams := findTypeDeclTypeParams(file, typeName); typeTypeParams != nil {
			return rewriteGenericMethod(fset, file, target, targetIdx, typeName, methodName, isPointer, typeTypeParams)
		}
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

	// Clear positions on the spliced decls so format.Node doesn't try to
	// align them against the parent file's fset — otherwise qualified
	// selectors like `Rect._real_Rect_Area` print split across lines.
	for _, d := range genFile.Decls {
		clearNodePositions(d)
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
// Uses the same AST-splice strategy as the non-generic path, with one
// extra step: clearPositions zeros every token.Pos on the spliced decls
// so the printer doesn't try to align them against the parent file's
// fset (which was the cause of mangled `sync.\n\tMap` formatting on an
// earlier attempt). reflect+sync imports are injected via ensureImport,
// which also works via position-cleared AST nodes.
func rewriteGenericFunction(fset *token.FileSet, file *ast.File, target *ast.FuncDecl, targetIdx int, funcName string) ([]byte, error) {
	params := ensureParamNames(target.Type.Params)
	hasResults := target.Type.Results != nil && len(target.Type.Results.List) > 0
	isVariadic := isVariadicFunc(target.Type)

	mockVarName := "Mock_" + funcName
	realFuncName := "_real_" + funcName
	realAliasName := "Real_" + funcName
	wrapperName := funcName

	// Print "[T, U any]" and "[T, U]" — the constrained form for decls
	// and the bare form for type-argument references inside the wrapper.
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

	// Generate the new decls as a package snippet, parse in a fresh
	// fset, then clear positions before splicing into the parent file.
	genSrc := fmt.Sprintf(`package %s

var %s sync.Map

func %s%s(%s) %s {
	%s
}

func %s%s(%s) %s {
	%s
}
`, file.Name.Name,
		mockVarName,
		realAliasName, typeParamsDecl, paramsSrc, resultsSrc, realAliasBody,
		wrapperName, typeParamsDecl, paramsSrc, resultsSrc, wrapperBody,
	)

	genFset := token.NewFileSet()
	genFile, err := parser.ParseFile(genFset, "", genSrc, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing generated generic wrapper (this is a bug in rewire):\n%s\nerror: %w", genSrc, err)
	}
	if len(genFile.Decls) != 3 {
		return nil, fmt.Errorf("internal error: expected 3 generated decls, got %d", len(genFile.Decls))
	}
	newMock := genFile.Decls[0]        // var Mock_X sync.Map
	newRealAlias := genFile.Decls[1]   // func Real_X[...](...)
	newWrapper := genFile.Decls[2]     // func X[...](...)
	clearNodePositions(newMock)
	clearNodePositions(newRealAlias)
	clearNodePositions(newWrapper)

	// Rename the original function to _real_<Name> so it remains a
	// generic function with the same body.
	target.Name.Name = realFuncName

	// Replace target in-place: Mock var + Real alias + wrapper + renamed
	// original. This must happen BEFORE ensureImport — if ensureImport
	// prepends a new import decl, targetIdx becomes stale and the splice
	// would skip the type decl and duplicate target.
	newDecls := make([]ast.Decl, 0, len(file.Decls)+3)
	newDecls = append(newDecls, file.Decls[:targetIdx]...)
	newDecls = append(newDecls, newMock)
	newDecls = append(newDecls, newRealAlias)
	newDecls = append(newDecls, newWrapper)
	newDecls = append(newDecls, target)
	newDecls = append(newDecls, file.Decls[targetIdx+1:]...)
	file.Decls = newDecls

	// Now that the splice is done and targetIdx is no longer needed,
	// merge reflect + sync into the target file's import block.
	ensureImport(file, "reflect")
	ensureImport(file, "sync")

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("formatting output: %w", err)
	}
	return buf.Bytes(), nil
}

// findTypeDeclTypeParams searches file for a top-level type declaration
// named typeName and returns its type parameter list, or nil if the type
// is not declared in the file or has no type parameters.
func findTypeDeclTypeParams(file *ast.File, typeName string) *ast.FieldList {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				continue
			}
			if ts.TypeParams != nil && ts.TypeParams.NumFields() > 0 {
				return ts.TypeParams
			}
			return nil
		}
	}
	return nil
}

// rewriteGenericMethod handles methods on generic types like
//
//	func (c *Container[T]) Add(v T)
//
// It emits a sync.Map-backed mock variable (one per method, not per
// instantiation), a wrapper method that dispatches via
// reflect.TypeOf((*Container[T]).Add).String(), and a free generic
// function Real_Container_Add[T any](c *Container[T], v T) so the
// codegen can materialize concrete Real_X[T1, T2, ...] values at
// compile time for each instantiation.
//
// The method itself can't declare type parameters (Go 1.18+ forbids it),
// so all type params come from the receiver's type declaration, passed
// in as typeTypeParams.
func rewriteGenericMethod(fset *token.FileSet, file *ast.File, target *ast.FuncDecl, targetIdx int, typeName, methodName string, isPointer bool, typeTypeParams *ast.FieldList) ([]byte, error) {
	params := ensureParamNames(target.Type.Params)
	hasResults := target.Type.Results != nil && len(target.Type.Results.List) > 0
	isVariadic := isVariadicFunc(target.Type)

	mockVarName := fmt.Sprintf("Mock_%s_%s", typeName, methodName)
	realFuncName := fmt.Sprintf("_real_%s_%s", typeName, methodName)
	realAliasName := fmt.Sprintf("Real_%s_%s", typeName, methodName)
	wrapperName := methodName

	// Type-parameter list from the receiver type: "[T any]" / "[K, V any]".
	typeParamsInnerSrc, err := fieldListToString(fset, typeTypeParams)
	if err != nil {
		return nil, fmt.Errorf("printing type params: %w", err)
	}
	typeParamsDecl := "[" + typeParamsInnerSrc + "]"

	var typeParamNames []string
	for _, field := range typeTypeParams.List {
		for _, n := range field.Names {
			typeParamNames = append(typeParamNames, n.Name)
		}
	}
	typeParamRef := "[" + strings.Join(typeParamNames, ", ") + "]"

	// Receiver expressions:
	//   recvTypeWithParams: "*Container[T]"   or  "Container[T]"
	//   methodExprStr:       "(*Container[T]).Add" or "Container[T].Add"
	var recvTypeWithParams, methodExprStr string
	if isPointer {
		recvTypeWithParams = "*" + typeName + typeParamRef
		methodExprStr = "(*" + typeName + typeParamRef + ")." + methodName
	} else {
		recvTypeWithParams = typeName + typeParamRef
		methodExprStr = typeName + typeParamRef + "." + methodName
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

	callArgs := buildCallArgs(params, isVariadic)

	// Receiver name (use the original if one was given, else a synthetic).
	recvField := target.Recv.List[0]
	recvName := "_rewire_recv"
	if len(recvField.Names) > 0 && recvField.Names[0].Name != "" {
		recvName = recvField.Names[0].Name
	}
	recvDeclSrc := fmt.Sprintf("(%s %s)", recvName, recvTypeWithParams)

	// Type of the mock function the wrapper expects: receiver prepended
	// to the method's params. Example: func(*Container[T], T)
	mockParamsStr := recvTypeWithParams
	if pOnly := typeOnlyFieldList(fset, params); pOnly != "" {
		mockParamsStr += ", " + pOnly
	}
	mockFnType := "func(" + mockParamsStr + ")"
	if hasResults {
		mockFnType = "func(" + mockParamsStr + ") " + resultsSrc
	}

	// When calling the mock, we pass the receiver first, then the params.
	mockCallArgs := recvName
	if callArgs != "" {
		mockCallArgs += ", " + callArgs
	}

	var wrapperBody, realAliasBody string
	if hasResults {
		wrapperBody = fmt.Sprintf(`if _rewire_raw, _rewire_ok := %s.Load(reflect.TypeOf(%s).String()); _rewire_ok {
		if _rewire_typed, _rewire_ok := _rewire_raw.(%s); _rewire_ok {
			return _rewire_typed(%s)
		}
	}
	return %s.%s(%s)`, mockVarName, methodExprStr, mockFnType, mockCallArgs, recvName, realFuncName, callArgs)
		realAliasBody = fmt.Sprintf("return %s.%s(%s)", recvName, realFuncName, callArgs)
	} else {
		wrapperBody = fmt.Sprintf(`if _rewire_raw, _rewire_ok := %s.Load(reflect.TypeOf(%s).String()); _rewire_ok {
		if _rewire_typed, _rewire_ok := _rewire_raw.(%s); _rewire_ok {
			_rewire_typed(%s)
			return
		}
	}
	%s.%s(%s)`, mockVarName, methodExprStr, mockFnType, mockCallArgs, recvName, realFuncName, callArgs)
		realAliasBody = fmt.Sprintf("%s.%s(%s)", recvName, realFuncName, callArgs)
	}

	// The Real_ alias is a free generic FUNCTION — takes the receiver as
	// first arg, carries the type-parameter list with its original
	// constraints, and forwards to the renamed method.
	realAliasParams := fmt.Sprintf("%s %s", recvName, recvTypeWithParams)
	if paramsSrc != "" {
		realAliasParams += ", " + paramsSrc
	}

	genSrc := fmt.Sprintf(`package %s

var %s sync.Map

func %s%s(%s) %s {
	%s
}

func %s %s(%s) %s {
	%s
}
`, file.Name.Name,
		mockVarName,
		realAliasName, typeParamsDecl, realAliasParams, resultsSrc, realAliasBody,
		recvDeclSrc, wrapperName, paramsSrc, resultsSrc, wrapperBody,
	)

	genFset := token.NewFileSet()
	genFile, err := parser.ParseFile(genFset, "", genSrc, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing generated generic-method wrapper (this is a bug in rewire):\n%s\nerror: %w", genSrc, err)
	}
	if len(genFile.Decls) != 3 {
		return nil, fmt.Errorf("internal error: expected 3 generated decls for generic method, got %d", len(genFile.Decls))
	}
	newMock := genFile.Decls[0]       // var Mock_X sync.Map
	newRealAlias := genFile.Decls[1]  // func Real_X[...]
	newWrapper := genFile.Decls[2]    // func (recv *Type[T]) X(...)
	clearNodePositions(newMock)
	clearNodePositions(newRealAlias)
	clearNodePositions(newWrapper)

	// Rename the original method to _real_Type_Method. It stays a method
	// with the original receiver (still `func (c *Container[T]) _real_X(...)`).
	target.Name.Name = realFuncName

	// Splice BEFORE ensureImport — see the comment in rewriteGenericFunction
	// for why (stale targetIdx if ensureImport prepends an import decl).
	newDecls := make([]ast.Decl, 0, len(file.Decls)+3)
	newDecls = append(newDecls, file.Decls[:targetIdx]...)
	newDecls = append(newDecls, newMock)
	newDecls = append(newDecls, newRealAlias)
	newDecls = append(newDecls, newWrapper)
	newDecls = append(newDecls, target)
	newDecls = append(newDecls, file.Decls[targetIdx+1:]...)
	file.Decls = newDecls

	ensureImport(file, "reflect")
	ensureImport(file, "sync")

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("formatting output: %w", err)
	}
	return buf.Bytes(), nil
}

// clearNodePositions neutralizes token.Pos fields on nodes reachable from
// n so that the printer doesn't try to resolve foreign fset positions when
// these nodes are spliced into a file from a different fset. Without it,
// qualified selectors like `sync.Map` or `Rect._real_X` end up split
// across lines because the printer sees the two halves as living on
// different logical rows.
//
// Caveat: some Pos fields carry *semantic* information via zero vs
// non-zero (e.g. CallExpr.Ellipsis signals variadic, GenDecl.Lparen
// signals parenthesized block form). We must not flatten those to zero,
// or we'd lose the semantic distinction. So: preserve zero-ness — fields
// that were NoPos stay NoPos, fields that were non-zero get set to Pos(1)
// (any stable non-zero value works since the actual location is what we
// want to discard).
//
// ast.Inspect follows only positional children, so it doesn't traverse
// the Obj/Scope back-references that would otherwise cause infinite loops.
func clearNodePositions(n ast.Node) {
	posType := reflect.TypeOf(token.NoPos)
	ast.Inspect(n, func(x ast.Node) bool {
		if x == nil {
			return false
		}
		v := reflect.ValueOf(x)
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return true
		}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if t.Field(i).Type != posType || !f.CanSet() {
				continue
			}
			if f.Int() != int64(token.NoPos) {
				f.SetInt(1)
			}
		}
		return true
	})
}

// ensureImport adds an import of pkgPath to file if it is not already
// present. Uses position-cleared AST nodes so the surrounding import
// block formats cleanly regardless of the parent file's fset state.
func ensureImport(file *ast.File, pkgPath string) {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) == pkgPath {
			return
		}
	}
	newSpec := &ast.ImportSpec{
		Path: &ast.BasicLit{
			Kind:  token.STRING,
			Value: fmt.Sprintf("%q", pkgPath),
		},
	}
	clearNodePositions(newSpec)

	// Extend the first existing import decl, or create one.
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			gen.Specs = append(gen.Specs, newSpec)
			// Force parenthesized block form if it wasn't already.
			if gen.Lparen == token.NoPos {
				gen.Lparen = token.Pos(1)
			}
			file.Imports = append(file.Imports, newSpec)
			return
		}
	}
	newDecl := &ast.GenDecl{
		Tok:    token.IMPORT,
		Lparen: token.Pos(1),
		Specs:  []ast.Spec{newSpec},
	}
	file.Decls = append([]ast.Decl{newDecl}, file.Decls...)
	file.Imports = append(file.Imports, newSpec)
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

// matchesReceiver checks if a receiver field list matches the expected type
// and pointer-ness. Accepts both plain types (`*Container`) and generic
// types (`*Container[T]` / `*Container[K, V]`) — the type argument list
// is stripped before matching the type name.
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
		recvType = starExpr.X
	}
	// Strip generic type arguments: `Container[T]` → `Container`.
	switch idx := recvType.(type) {
	case *ast.IndexExpr:
		recvType = idx.X
	case *ast.IndexListExpr:
		recvType = idx.X
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
