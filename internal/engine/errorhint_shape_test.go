package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is a static property of error construction, not a claim that a dynamic
// hint expression is nonempty at runtime. Dynamic helpers need behavioral tests.
func TestStructuredErrorLiteralsAlwaysSpecifyHint(t *testing.T) {
	for _, dir := range []string{".", "../mcp", "../core"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok && file.Name.Name == "core" {
					if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "errf" && len(call.Args) > 1 {
						if value, ok := call.Args[1].(*ast.BasicLit); ok && value.Kind == token.STRING {
							decoded, err := strconv.Unquote(value.Value)
							if err != nil || strings.TrimSpace(decoded) == "" {
								t.Errorf("%s: empty errf hint", fset.Position(value.Pos()))
							}
						}
					}
				}
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				isError := false
				switch typ := literal.Type.(type) {
				case *ast.SelectorExpr:
					pkg, ok := typ.X.(*ast.Ident)
					isError = ok && pkg.Name == "core" && typ.Sel.Name == "Error"
				case *ast.Ident:
					isError = file.Name.Name == "core" && typ.Name == "Error"
				}
				if !isError {
					return true
				}
				var hint ast.Expr
				for _, elem := range literal.Elts {
					if kv, ok := elem.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Hint" {
							hint = kv.Value
						}
					}
				}
				if hint == nil {
					t.Errorf("%s: structured error omitted Hint", fset.Position(literal.Pos()))
					return true
				}
				if value, ok := hint.(*ast.BasicLit); ok && value.Kind == token.STRING {
					decoded, err := strconv.Unquote(value.Value)
					if err != nil || strings.TrimSpace(decoded) == "" {
						t.Errorf("%s: empty Hint", fset.Position(value.Pos()))
					}
				}
				return true
			})
		}
	}
}
