package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Nothing exported from this package asks about a directory without
// asking which machine.
//
// A path is evidence on ONE computer: /workspace/repo on two machines is
// two unrelated directories. That sentence has been learned here six
// separate times, and five of those were the review finding it missing
// somewhere new, one call site per round:
//
//   - round three, the claim rule reading the holder's current host
//   - round eight, differentProjects on the Git directory
//   - round thirty-six, hook resolution (AgentForHookOn)
//   - round sixty-two, sameRepoIdentity on a `file:` remote
//   - round sixty-four, differentProjects on the same remote
//   - round sixty-five, the hook-health diagnostic and the compact board
//
// Each was fixed where it was named and the next round found another.
// The sweep after the third one in a row deleted every unscoped lookup
// rather than documenting it: ActiveAgentsIn, ActiveAgentIDsIn,
// AgentsIn, ReattachableIn and AgentForHook are gone, and their `On`
// siblings take the host. A caller that genuinely has no machine to
// state passes "", which every one of them reads as "no evidence of
// difference", and has to type it.
//
// This is what stops the next one. It is deliberately about the SHAPE of
// the signature rather than the behaviour: a rule enforced by reading
// what somebody wrote is a rule somebody can forget, and the exported
// surface is the one place a new caller looks.
func TestNoExportedLookupTakesADirectoryWithoutAMachine(t *testing.T) {
	// Parameters that name a place on one computer. `path` is not here:
	// a claim's path is compared against other claims, which carry their
	// own host, and the comparison is what round three fixed.
	located := map[string]bool{"cwd": true, "dir": true, "root": true}
	// A parameter that says which computer.
	machine := map[string]bool{"host": true, "hostID": true, "hostId": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var checked int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			var params, all []string
			for _, p := range fn.Type.Params.List {
				for _, id := range p.Names {
					all = append(all, id.Name)
					if located[id.Name] {
						params = append(params, id.Name)
					}
				}
			}
			if len(params) == 0 {
				continue
			}
			checked++
			stated := false
			for _, n := range all {
				if machine[n] {
					stated = true
				}
			}
			if !stated {
				t.Errorf("%s:%d: %s takes %v and no host.\n"+
					"  A path is evidence on one computer, and this package's exported "+
					"surface is where a new caller looks for the answer. Add a host "+
					"parameter; a caller with none to state passes \"\", which every "+
					"lookup here reads as no evidence of difference.",
					name, fset.Position(fn.Pos()).Line, fn.Name.Name, params)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no exported method here takes a directory at all, so this test measured " +
			"nothing: it is looking in the wrong place, or the parameter names it knows " +
			"are no longer the ones used")
	}
}
