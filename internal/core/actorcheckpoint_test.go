package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every actor op that returns early from the dispatcher sets the durable
// checkpoint itself.
//
// The common path assigns `l.LastCoordination = now` under a comment saying
// every ledgered actor op refreshes it. Three handlers return straight out of
// the switch and never reach that line, so they have to do it themselves, and
// all three shipped without: adopt_agent, then claim_coordinator and prune_own.
// The daemon's derived `seen` map hides it while the process runs and is
// deliberately not replayable, so the symptom only appears after a restart, as
// an agent swept stale moments after it did something.
//
// A behavioural test would have to construct a valid op for each, and the next
// early return added to that switch would still slip past. This reads the
// switch instead: any case whose body is a bare `return s.applyX(...)` must
// name a handler that assigns LastCoordination. It fails when somebody adds the
// fourth, which is the point, because this is the third.
func TestEveryEarlyReturningActorOpKeepsTheCheckpoint(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(dir, "apply.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Every method body in the package, so a handler defined in another file
	// (adopt.go) is found too.
	bodies := map[string]*ast.FuncDecl{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil {
				bodies[fd.Name.Name] = fd
			}
		}
	}

	var apply *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Apply" && fd.Recv != nil {
			apply = fd
		}
	}
	if apply == nil {
		t.Fatal("Apply is not in apply.go any more: this guard is reading the wrong thing")
	}

	found := 0
	ast.Inspect(apply, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok || len(cc.Body) != 1 {
			return true
		}
		ret, ok := cc.Body[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// ONLY THE ONES THAT TAKE THE ACTOR. sweep, prune, grant_role and
		// mark_delivered also return early, and correctly never touch a
		// checkpoint: they are the daemon's and the human's ops, resolved
		// before any actor and holding no agent to refresh. The first draft of
		// this guard flagged all four, which is a guard reporting a defect that
		// is not there, and that is worse than no guard.
		if !passesActor(call) {
			return true
		}
		handler := sel.Sel.Name
		found++
		fd, ok := bodies[handler]
		if !ok {
			t.Errorf("%s returns %s, which this guard cannot find", caseName(cc), handler)
			return true
		}
		if !assignsCheckpoint(fd) {
			t.Errorf("%s returns from the dispatcher through %s, which never sets "+
				"LastCoordination. The common path's assignment is below that return, "+
				"so this op leaves the actor's durable checkpoint where it was: after "+
				"a restart the agent is judged against the time before it acted, and "+
				"can be swept stale immediately. Set it in the handler, as "+
				"applyAdoptAgent does.", caseName(cc), handler)
		}
		return true
	})
	if found == 0 {
		t.Error("no early-returning case found in Apply's switch: either they are all " +
			"gone, in which case delete this, or this guard has stopped matching them " +
			"and is now watching nothing")
	}
	t.Logf("checked %d early-returning actor op(s)", found)
}

// passesActor reports whether this handler is handed the resolved actor, which
// is what makes it an actor op and gives it a checkpoint to keep.
func passesActor(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		if id, ok := a.(*ast.Ident); ok && id.Name == "l" {
			return true
		}
	}
	return false
}

func caseName(cc *ast.CaseClause) string {
	var names []string
	for _, e := range cc.List {
		if id, ok := e.(*ast.Ident); ok {
			names = append(names, id.Name)
		}
	}
	if len(names) == 0 {
		return "default:"
	}
	return "case " + strings.Join(names, ", ")
}

func assignsCheckpoint(fd *ast.FuncDecl) bool {
	assigned := false
	ast.Inspect(fd, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "LastCoordination" {
				assigned = true
			}
		}
		return true
	})
	return assigned
}
