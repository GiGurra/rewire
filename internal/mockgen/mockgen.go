package mockgen

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

// GenerateMock generates a mock struct implementing the named interface
// found in src. The generated code uses outputPkg as its package name.
func GenerateMock(src []byte, interfaceName string, outputPkg string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	// Find the interface
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
			iface = ifaceType
		}
	}
	if iface == nil {
		return nil, fmt.Errorf("interface %q not found", interfaceName)
	}

	// Build import map: local name → import path
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

	// Extract methods from interface
	type method struct {
		name       string
		params     string // "name string, age int"
		results    string // "(string, error)" or "string" or ""
		paramNames string // "name, age"
		funcType   string // "func(name string) string"
		hasResults bool
		isVariadic bool
	}

	var methods []method
	usedPkgs := map[string]bool{} // package local names referenced in types

	for _, field := range iface.Methods.List {
		funcType, ok := field.Type.(*ast.FuncType)
		if !ok || len(field.Names) == 0 {
			continue // skip embedded interfaces
		}

		methodName := field.Names[0].Name
		params := ensureParamNames(funcType.Params)
		isVariadic := isVariadicFunc(funcType)

		paramsSrc := fieldListToString(fset, params)
		paramNamesSrc := buildCallArgs(params, isVariadic)

		hasResults := funcType.Results != nil && len(funcType.Results.List) > 0
		resultsSrc := ""
		if hasResults {
			resultsSrc = resultsToString(fset, funcType.Results)
		}

		funcTypeSrc := "func(" + paramsSrc + ")"
		if resultsSrc != "" {
			funcTypeSrc += " " + resultsSrc
		}

		// Track which imported packages are used
		collectPkgRefs(funcType, usedPkgs)

		methods = append(methods, method{
			name:       methodName,
			params:     paramsSrc,
			results:    resultsSrc,
			paramNames: paramNamesSrc,
			funcType:   funcTypeSrc,
			hasResults: hasResults,
			isVariadic: isVariadic,
		})
	}

	// Generate source
	mockName := "Mock" + interfaceName

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", outputPkg)

	// Imports — only include packages actually referenced in method signatures
	var usedImports []string
	for localName := range usedPkgs {
		if path, ok := imports[localName]; ok {
			segments := strings.Split(path, "/")
			defaultName := segments[len(segments)-1]
			if localName != defaultName {
				usedImports = append(usedImports, fmt.Sprintf("%s %q", localName, path))
			} else {
				usedImports = append(usedImports, fmt.Sprintf("%q", path))
			}
		}
	}
	if len(usedImports) > 0 {
		b.WriteString("import (\n")
		for _, imp := range usedImports {
			fmt.Fprintf(&b, "\t%s\n", imp)
		}
		b.WriteString(")\n\n")
	}

	// Struct
	fmt.Fprintf(&b, "type %s struct {\n", mockName)
	for _, m := range methods {
		fmt.Fprintf(&b, "\t%sFunc %s\n", m.name, m.funcType)
	}
	b.WriteString("}\n\n")

	// Method implementations
	for _, m := range methods {
		if m.hasResults {
			// Use named return params so bare return gives zero values
			namedResults := addResultNames(fset, m.results)
			fmt.Fprintf(&b, "func (m *%s) %s(%s) %s {\n", mockName, m.name, m.params, namedResults)
			fmt.Fprintf(&b, "\tif m.%sFunc != nil {\n", m.name)
			fmt.Fprintf(&b, "\t\treturn m.%sFunc(%s)\n", m.name, m.paramNames)
			b.WriteString("\t}\n")
			b.WriteString("\treturn\n")
		} else {
			fmt.Fprintf(&b, "func (m *%s) %s(%s) {\n", mockName, m.name, m.params)
			fmt.Fprintf(&b, "\tif m.%sFunc != nil {\n", m.name)
			fmt.Fprintf(&b, "\t\tm.%sFunc(%s)\n", m.name, m.paramNames)
			b.WriteString("\t}\n")
		}
		b.WriteString("}\n\n")
	}

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("formatting generated mock (this is a bug in rewire):\n%s\nerror: %w", b.String(), err)
	}
	return formatted, nil
}

// addResultNames wraps result types with generated names for bare return support.
// "(string, error)" → "(_r0 string, _r1 error)"
// "string" → "(_r0 string)"
func addResultNames(fset *token.FileSet, resultsSrc string) string {
	// Parse as a func type to extract results
	tmp := "package p\ntype x " + "func() " + resultsSrc
	f, err := parser.ParseFile(fset, "", tmp, 0)
	if err != nil {
		return resultsSrc // fall back to original
	}

	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			funcType, ok := typeSpec.Type.(*ast.FuncType)
			if !ok || funcType.Results == nil {
				continue
			}

			var parts []string
			idx := 0
			for _, field := range funcType.Results.List {
				typStr := nodeToString(fset, field.Type)
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				for range count {
					parts = append(parts, fmt.Sprintf("_r%d %s", idx, typStr))
					idx++
				}
			}
			return "(" + strings.Join(parts, ", ") + ")"
		}
	}
	return resultsSrc
}

// collectPkgRefs walks an AST node and collects package-qualified identifiers.
func collectPkgRefs(node ast.Node, pkgs map[string]bool) {
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok {
			pkgs[ident.Name] = true
		}
		return true
	})
}

// InferPackageName extracts the package name from Go source.
func InferPackageName(src []byte) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.PackageClauseOnly)
	if err != nil {
		return "mock"
	}
	return f.Name.Name
}

// --- helpers (same patterns as rewriter, kept local to avoid cross-package deps) ---

func ensureParamNames(fields *ast.FieldList) *ast.FieldList {
	if fields == nil {
		return fields
	}
	idx := 0
	result := &ast.FieldList{Opening: fields.Opening, Closing: fields.Closing}
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

func fieldListToString(fset *token.FileSet, fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fields.List {
		typStr := nodeToString(fset, f.Type)
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
	return strings.Join(parts, ", ")
}

func resultsToString(fset *token.FileSet, fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	s := fieldListToString(fset, fields)
	if len(fields.List) > 1 || len(fields.List[0].Names) > 0 {
		return "(" + s + ")"
	}
	return s
}

func buildCallArgs(params *ast.FieldList, isVariadic bool) string {
	names := paramNames(params)
	if isVariadic && len(names) > 0 {
		names[len(names)-1] = names[len(names)-1] + "..."
	}
	return strings.Join(names, ", ")
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

func nodeToString(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, node)
	return buf.String()
}
