package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A corrective call naming a nonexistent CLI verb cannot correct anything.
// This is a static property of production string constants; enter at the AST,
// not at a hand-maintained list of the hints we happen to remember.
func TestBacktickedDibsCommandsNameDispatchedVerbs(t *testing.T) {
	known := map[string]bool{}
	for _, verb := range dispatchedVerbs(t) {
		known[verb] = true
	}
	command := regexp.MustCompile("`dibs[ \\t]+([a-z][a-z0-9-]*)\\b")
	set := token.NewFileSet()
	count := 0
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := parser.ParseFile(set, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(src, func(node ast.Node) bool {
				expr, ok := node.(ast.Expr)
				if !ok {
					return true
				}
				value, ok := constantHintString(expr)
				if !ok {
					return true
				}
				for _, match := range command.FindAllStringSubmatch(value, -1) {
					count++
					if !known[match[1]] {
						t.Errorf("%s: backticked hint names nonexistent command %q; correct the hint, not the command table",
							set.Position(node.Pos()), "dibs "+match[1])
					}
				}
				return false // a concatenation's children are already covered
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if count == 0 {
		t.Fatal("no backticked CLI hints examined: this guard would be vacuous")
	}
	t.Logf("checked %d backticked CLI commands against %d dispatched verbs (aliases included)", count, len(known))
}

// Fold literal concatenations too: splitting a command across two Go strings
// must not hide an invalid verb. Variables and computed commands are not static
// evidence; inspect their literal subexpressions, without pretending to know
// their runtime values.
func constantHintString(expr ast.Expr) (string, bool) {
	switch n := expr.(type) {
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(n.Value)
		return value, err == nil
	case *ast.ParenExpr:
		return constantHintString(n.X)
	case *ast.BinaryExpr:
		if n.Op == token.ADD {
			left, lok := constantHintString(n.X)
			right, rok := constantHintString(n.Y)
			return left + right, lok && rok
		}
	}
	return "", false
}

func TestConstantHintStringIncludesConcatenatedCommand(t *testing.T) {
	expr, err := parser.ParseExpr("\"run `dibs ser\" + (\"vice start`\")")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := constantHintString(expr)
	if !ok || value != "run `dibs service start`" {
		t.Fatalf("split command disappeared: %q, %v", value, ok)
	}
}
