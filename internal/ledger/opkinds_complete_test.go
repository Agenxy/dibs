package ledger_test

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

// Every op kind declared in core must appear in the frozen table.
//
// That table calls itself the single authoritative list of ledger vocabulary,
// and it was missing twelve kinds, including `respond`, `ack`, `bind_session`
// and four space operations. A rename of any one of them left the frozen test
// green while every ledger containing it stopped replaying: the guard against
// silent data loss, silently not guarding.
//
// A LIST SOMEBODY MUST REMEMBER IS AS GOOD AS THE MEMORY. This repository has
// said that about itself twice, in the fold-safety test and in the doc-count
// gate, and then maintained this one by hand anyway. So this reads the SOURCE:
// every `Op… = "…"` constant in internal/core has to be named in the table, and
// adding a kind without freezing it fails here rather than in somebody's
// ledger. Found by the pre-release review.
func TestEveryOpKindIsFrozen(t *testing.T) {
	declared := opKindsInCore(t)
	if len(declared) < 30 {
		t.Fatalf("found only %d op kinds in internal/core, which cannot be right: "+
			"the declaration shape has changed and this check is reading nothing",
			len(declared))
	}

	frozen, err := os.ReadFile("wireformat_test.go")
	if err != nil {
		t.Fatalf("reading the frozen table: %v", err)
	}
	table := string(frozen)

	var missing []string
	for name, value := range declared {
		// The pairing in that table is `"OpName": {core.OpName, "value"}`, so
		// both halves have to be present for it to be checked at all.
		if !strings.Contains(table, "core."+name+",") || !strings.Contains(table, `"`+value+`"`) {
			missing = append(missing, name+" = "+strconv.Quote(value))
		}
	}
	if len(missing) > 0 {
		t.Errorf("these op kinds are declared in internal/core and not frozen:\n  %s\n"+
			"  Each is a string a ledger already contains and Apply matches by value, "+
			"so renaming one is silent data loss on every board that has it. Add each "+
			"to the table in wireformat_test.go.", strings.Join(missing, "\n  "))
	}
}

// opKindsInCore returns every Op… string constant declared in the core package.
func opKindsInCore(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "core")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	// One file at a time rather than parser.ParseDir, which is deprecated for
	// ignoring build tags. Nothing here depends on package grouping: every
	// declaration wanted is a plain const in one of these files.
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", e.Name(), perr)
		}
		{
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
						continue
					}
					name := vs.Names[0].Name
					// `Open` is a different constant that merely starts the same
					// way; an op kind is Op followed by an upper-case letter.
					if !strings.HasPrefix(name, "Op") || len(name) < 3 || name[2] < 'A' || name[2] > 'Z' {
						continue
					}
					lit, ok := vs.Values[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err == nil && v != "" {
						out[name] = v
					}
				}
			}
		}
	}
	return out
}
