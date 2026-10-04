package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A helper returning a closed listener's address creates a static ownership
// gap. Returning a live listener or a Reservation keeps ownership explicit.
func TestNoTestHelperReturnsAClosedListenerAddress(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && d.IsDir() && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			var body *ast.BlockStmt
			name := ""
			switch f := n.(type) {
			case *ast.FuncDecl:
				body = f.Body
				name = f.Name.Name
			case *ast.FuncLit:
				body = f.Body
			}
			if body != nil && returnsClosedAddress(body) {
				t.Errorf("%s: helper closes a listener then returns its address; use testport.Reserve",
					fset.Position(body.Pos()))
			}
			if body != nil && name != "" {
				ast.Inspect(body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "ReleaseForOutage" {
						return true
					}
					allowed := map[string]string{
						"bridge_startup_test.go": "lateBridgeStartup",
						"bridgeoutage_test.go":   "TestTheBridgeSurvivesADaemonThatGoesAwayAndComesBack",
						"await_test.go":          "TestAwaitSaysTheDaemonIsGoneWithItsOwnExitCode",
						"reserve_test.go":        "TestOutageSelectionChangesAddressOnlyBeforeTheTestStarts",
					}
					if allowed[filepath.Base(path)] != name {
						t.Errorf("%s: only the named connection-refused fixtures may release for an outage",
							fset.Position(call.Pos()))
					}
					return true
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func returnsClosedAddress(body *ast.BlockStmt) bool {
	addresses := map[string]string{}
	closed := map[string]bool{}
	returned := map[string]bool{}
	direct := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		switch v := n.(type) {
		case *ast.AssignStmt:
			if len(v.Lhs) == 1 && len(v.Rhs) == 1 {
				id, ok := v.Lhs[0].(*ast.Ident)
				if ok {
					if receiver := addressReceiver(v.Rhs[0]); receiver != "" {
						addresses[id.Name] = receiver
					}
				}
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" {
				if id, ok := sel.X.(*ast.Ident); ok {
					closed[id.Name] = true
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range v.Results {
				if id, ok := expr.(*ast.Ident); ok {
					returned[id.Name] = true
				}
				if receiver := addressReceiver(expr); receiver != "" && closed[receiver] {
					direct = true
				}
			}
		}
		return true
	})
	for addr, receiver := range addresses {
		if returned[addr] && closed[receiver] {
			return true
		}
	}
	return direct
}

func TestClosedAddressGuardRecognizesAliasesAndLiveOwnership(t *testing.T) {
	for _, c := range []struct {
		source string
		bad    bool
	}{
		{`func fixture() string { ln, _ := net.Listen("tcp", "127.0.0.1:0"); a := ln.Addr().String(); ln.Close(); return a }`, true},
		{`func fixture() string { ln, _ := net.Listen("tcp", "127.0.0.1:0"); ln.Close(); return ln.Addr().String() }`, true},
		{`func fixture() string { ln, _ := net.Listen("tcp", "127.0.0.1:0"); t.Cleanup(func() { ln.Close() }); return ln.Addr().String() }`, false},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\n"+c.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := returnsClosedAddress(file.Decls[0].(*ast.FuncDecl).Body); got != c.bad {
			t.Fatalf("guard returned %v for %s", got, c.source)
		}
	}
}

func addressReceiver(expr ast.Expr) string {
	outer, ok := expr.(*ast.CallExpr)
	if !ok {
		return ""
	}
	str, ok := outer.Fun.(*ast.SelectorExpr)
	if !ok || str.Sel.Name != "String" {
		return ""
	}
	inner, ok := str.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	addr, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok || addr.Sel.Name != "Addr" {
		return ""
	}
	id, ok := addr.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name
}
