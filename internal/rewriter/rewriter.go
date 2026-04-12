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

	// Find the target function declaration
	var target *ast.FuncDecl
	var targetIdx int
	for i, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == funcName {
			if fd.Recv != nil {
				return nil, fmt.Errorf("rewire does not support methods yet (function %q has a receiver)", funcName)
			}
			target = fd
			targetIdx = i
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("function %q not found", funcName)
	}

	// Extract signature info we need for code generation
	params := ensureParamNames(target.Type.Params)
	funcTypeSrc, err := nodeToString(fset, target.Type)
	if err != nil {
		return nil, fmt.Errorf("printing function type: %w", err)
	}

	paramCallArgs := paramNames(params)
	hasResults := target.Type.Results != nil && len(target.Type.Results.List) > 0
	isVariadic := isVariadicFunc(target.Type)

	// Build the three replacement declarations as source text,
	// then parse them back to AST nodes for clean insertion.
	mockVarName := "Mock_" + funcName
	realFuncName := "_real_" + funcName

	var callArgs string
	if isVariadic {
		// Last param needs ... spread
		names := paramNames(params)
		if len(names) > 0 {
			last := names[len(names)-1]
			names[len(names)-1] = last + "..."
		}
		callArgs = strings.Join(names, ", ")
	} else {
		callArgs = strings.Join(paramCallArgs, ", ")
	}

	var mockBody string
	if hasResults {
		mockBody = fmt.Sprintf(`if f := %s; f != nil {
		return f(%s)
	}
	return %s(%s)`, mockVarName, callArgs, realFuncName, callArgs)
	} else {
		mockBody = fmt.Sprintf(`if f := %s; f != nil {
		f(%s)
		return
	}
	%s(%s)`, mockVarName, callArgs, realFuncName, callArgs)
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

	// funcType for the var is the same as the function signature but as a func type
	genSrc := fmt.Sprintf(`package %s

var %s %s

func %s(%s) %s {
	%s
}
`, file.Name.Name, mockVarName, funcTypeSrc, funcName, paramsSrc, resultsSrc, mockBody)

	genFset := token.NewFileSet()
	genFile, err := parser.ParseFile(genFset, "", genSrc, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing generated wrapper (this is a bug in rewire):\n%s\nerror: %w", genSrc, err)
	}

	// Rename the original function to _real_<Name>
	target.Name.Name = realFuncName

	// Replace the original decl with: var + wrapper + renamed original
	// genFile.Decls[0] = var, genFile.Decls[1] = wrapper func
	newDecls := make([]ast.Decl, 0, len(file.Decls)+2)
	newDecls = append(newDecls, file.Decls[:targetIdx]...)
	newDecls = append(newDecls, genFile.Decls[0]) // var Mock_X
	newDecls = append(newDecls, genFile.Decls[1]) // func X (wrapper)
	newDecls = append(newDecls, target)            // func _real_X (original, renamed)
	newDecls = append(newDecls, file.Decls[targetIdx+1:]...)
	file.Decls = newDecls

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("formatting output: %w", err)
	}
	return buf.Bytes(), nil
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
