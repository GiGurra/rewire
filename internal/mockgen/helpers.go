package mockgen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
)

// ShortImportPathHash returns an 8-hex-char slice of SHA256(importPath),
// suitable for embedding into a generated Go identifier. It's
// deterministic (same path always hashes the same, so generated struct
// names are stable across runs and across parallel compile workers)
// and short enough to keep mangled struct names readable while
// disambiguating two packages that happen to share the same declared
// `package X` identifier.
func ShortImportPathHash(importPath string) string {
	sum := sha256.Sum256([]byte(importPath))
	return hex.EncodeToString(sum[:4]) // 8 hex chars → 32 bits → collision-free for any realistic module graph
}

// addResultNames wraps result types with generated names for bare return support.
// "(string, error)" → "(_r0 string, _r1 error)"
// "string" → "(_r0 string)"
func addResultNames(fset *token.FileSet, resultsSrc string) string {
	tmp := "package p\ntype x " + "func() " + resultsSrc
	f, err := parser.ParseFile(fset, "", tmp, 0)
	if err != nil {
		return resultsSrc
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

// ensureParamNames assigns synthetic names (p0, p1, ...) to any unnamed
// parameters in fields, returning a new FieldList with names guaranteed.
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

// fieldListToString prints a parameter or result list as "name type, name type"
// (or just "type, type" for unnamed fields).
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

// resultsToString prints a function's result list, parenthesizing when there
// are multiple results or a single named result.
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

// buildCallArgs returns the comma-separated parameter names suitable for
// forwarding the call, with a trailing "..." spread on the last argument
// when the function is variadic.
func buildCallArgs(params *ast.FieldList, isVariadic bool) string {
	names := paramNames(params)
	if isVariadic && len(names) > 0 {
		names[len(names)-1] = names[len(names)-1] + "..."
	}
	return strings.Join(names, ", ")
}

// paramNames returns the flat list of parameter names from a field list.
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

// isVariadicFunc reports whether the function's last parameter is variadic.
func isVariadicFunc(ft *ast.FuncType) bool {
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return false
	}
	last := ft.Params.List[len(ft.Params.List)-1]
	_, ok := last.Type.(*ast.Ellipsis)
	return ok
}

// nodeToString prints an AST node using the standard go/printer.
func nodeToString(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, node)
	return buf.String()
}
