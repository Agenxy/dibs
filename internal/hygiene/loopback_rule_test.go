package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// One place decides whether an address is confined to this machine.
//
// There were five. `internal/transport.IsLoopback`, and a hand-copy in
// `cmd/dibd`, two in `cmd/dibs` and one more in the join recipe, each a little
// different from the others: one missed the wildcard bind, one did not strip
// the brackets off an IPv6 host, one was dead code carrying a comment that
// described behaviour nothing used. They disagreed in the way copies always
// do, one at a time and only under an input nobody had tried: `:4777` is a bind
// on every interface, the copy in the join recipe read it as loopback, and a
// board deliberately bound wide was handed an ssh-forward recipe it could not
// follow. That was fixed IN THE COPY, and the comment recording the fix said
// in as many words that the shared code already read it correctly. Two of the
// remaining copies carried a comment naming internal/transport as the real
// rule while restating it anyway.
//
// So this is not a style check. The answer decides whether Dibs serves
// plaintext or TLS, whether a joining machine is told to open a tunnel or trust
// a certificate, and whether a board is published at an address the operator
// thinks is private. A second opinion about it is a second answer.
//
// Shape, not memory: any function that splits a host and port and then asks
// whether the result is loopback is deciding this question, wherever it lives
// and whatever it is called.
func TestOnlyOnePlaceDecidesWhetherAnAddressIsLoopback(t *testing.T) {
	// internal/mcp is the ONE exception, and it is a different question.
	//
	// `transport.IsLoopback` reads what the OPERATOR configured: "localhost"
	// there is the operator naming their own machine, and it is loopback.
	// internal/mcp reads the PEER's address off an inbound request and uses the
	// answer to decide what that peer is trusted with, so a name is not proof:
	// resolving it would be a DNS call on the request path, and a caller who
	// controls their own resolver would control the answer. Its own test pins
	// `localhost:4777` to false for exactly that reason. Merging the two would
	// hand an attacker-supplied name the standing that belongs to the
	// operator's own configuration, so these must stay two functions, and this
	// is the note that stops a later sweep helpfully folding them.
	exempt := map[string]string{
		filepath.Join("..", "mcp", "identity.go"): "peer address, where a NAME is not proof",
	}

	root := filepath.Join("..", "..")
	var found int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "bin", "dist", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The rule's own home.
		if strings.Contains(filepath.ToSlash(path), "internal/transport/") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file this guard cannot parse is a file it cannot clear, and
			// skipping it quietly is how a scan passes over the copy it was
			// written to find. The build would catch a broken file too, but not
			// in this test's report, and this one is about what was NOT seen.
			t.Errorf("%s: cannot be parsed, so this guard cannot vouch for it: %v", path, perr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, isFunc := n.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				return true
			}
			if !splitsHostPort(fn.Body) || !asksIsLoopback(fn.Body) {
				return true
			}
			found++
			rel, _ := filepath.Rel(root, path)
			if why, ok := exempt[filepath.Join("..", strings.TrimPrefix(filepath.ToSlash(rel), "internal/"))]; ok {
				t.Logf("%s: %s is the documented exception (%s)", rel, fn.Name.Name, why)
				return true
			}
			t.Errorf("%s: %s splits a host and port and then decides loopback itself. "+
				"That question has one answer and it lives in internal/transport.IsLoopback; "+
				"five copies of it disagreed one input at a time. Call it instead.",
				rel, fn.Name.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A walk that visited nothing passes vacuously, which is the failure mode
	// of every tree-scanning guard in this repository.
	if found == 0 {
		t.Fatal("this guard found no host:port loopback test anywhere, not even the " +
			"documented exception in internal/mcp: it is scanning the wrong tree and " +
			"would pass however many copies came back")
	}
}

func splitsHostPort(body *ast.BlockStmt) bool {
	return calls(body, func(sel *ast.SelectorExpr) bool {
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "net" && sel.Sel.Name == "SplitHostPort"
	})
}

func asksIsLoopback(body *ast.BlockStmt) bool {
	return calls(body, func(sel *ast.SelectorExpr) bool { return sel.Sel.Name == "IsLoopback" })
}

func calls(body *ast.BlockStmt, match func(*ast.SelectorExpr) bool) bool {
	var hit bool
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && match(sel) {
			hit = true
		}
		return !hit
	})
	return hit
}
