package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every parameter a tool advertises must reach a handler that reads it.
//
// A schema is a PROMISE. An agent reads these descriptions and nothing else —
// it cannot see the handler, so a documented parameter that no code consumes is
// indistinguishable, from the outside, from one that works. The agent supplies
// it, the call succeeds, and the effect it was told to expect silently does not
// happen. That is worse than not offering the parameter at all: the agent has
// no way to discover it was ignored, and will keep supplying it.
//
// This is the codebase's most repeated defect. `core.Admit` sat uncalled;
// `vouch_child` was implemented and never declared; the duty-cycle rung was
// unit-tested and unreachable from Classify; `engine.Children` existed with no
// caller, which made a blocked child's state unreadable. Each was found by
// hand, late, after being reported as finished. Reviewing for it does not work,
// because the whole failure mode is that everything LOOKS present.
//
// The check runs against the shipped values rather than a copy: the parameters
// come out of toolDefs itself, and the fields out of the real toolArgs by
// reflection, so a schema and a struct that drift apart cannot both satisfy it.
func TestEveryDeclaredParameterIsReadByAHandler(t *testing.T) {
	// json tag -> Go field name, from the struct the decoder actually fills.
	field := map[string]string{}
	rt := reflect.TypeOf(toolArgs{})
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			field[name] = rt.Field(i).Name
		}
	}

	read := fieldsReadIn(t, ".")

	for _, tool := range toolDefs {
		name, _ := tool["name"].(string)
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			continue
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		for param := range props {
			// The token is threaded by the dispatcher rather than any one
			// handler, so it is authorisation rather than an argument.
			if param == "token" {
				continue
			}
			f, declared := field[param]
			if !declared {
				t.Errorf("%s advertises %q, but toolArgs has no field with that json tag —\n"+
					"  the decoder discards it, so an agent that supplies it is silently ignored",
					name, param)
				continue
			}
			if !read[f] {
				t.Errorf("%s advertises %q and toolArgs.%s is decoded, but nothing reads it —\n"+
					"  the parameter is documented, accepted, and has no effect. Either wire it\n"+
					"  through or stop advertising it; leaving it is a promise to the agent that\n"+
					"  it has no way to discover is false", name, param, f)
			}
		}
	}
}

// fieldsReadIn returns the toolArgs fields some expression in the package
// actually reads.
//
// Parsed rather than grepped so that a mention inside a comment, a string, or
// the struct declaration itself does not count as a use — those are exactly the
// places a field name appears when nobody consumes it. Only a selector on the
// right of a dot counts, which is how a field is read.
func fieldsReadIn(t *testing.T, dir string) map[string]bool {
	t.Helper()
	// Tests are excluded on purpose. A field read only by its own test is still
	// dead in the shipped binary, and that is exactly the shape being hunted —
	// counting test reads would let a parameter pass this check while doing
	// nothing for any agent that supplies it.
	sources, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", src, err)
		}
		files = append(files, f)
	}
	// Guard the guard: an empty file list would make every field look unread,
	// which fails loudly — but a glob that silently matched nothing while the
	// check still passed is the failure worth preventing here.
	if len(files) == 0 {
		t.Fatalf("no non-test sources found in %s; this check would be vacuous", dir)
	}

	read := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// a.Foo where a is the decoded args. Named by identifier rather than
			// resolved through types: this package uses one receiver name for
			// them throughout, and a types-based check would need the whole
			// build for no extra precision here.
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "a" {
				read[sel.Sel.Name] = true
			}
			return true
		})
	}
	return read
}
