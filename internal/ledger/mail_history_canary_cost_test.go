package ledger

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Whole-board serialization is a static cost: production bootstrap must have
// no encoding call. Actual private-fold equality remains a serving-path test.
func TestHistoryProductionBootstrapDoesNotSerializePrivateBoard(t *testing.T) {
	source, err := parser.ParseFile(token.NewFileSet(), "mail_history_bootstrap.go", nil, 0)
	if err != nil {
		t.Fatal("setup:", err)
	}
	ast.Inspect(source, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && pkg.Name == "json" && selector.Sel.Name == "Marshal" {
			t.Error("production bootstrap serialized private canonical board")
		}
		return true
	})
}
