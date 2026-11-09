// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/procexec"
)

// This property is static: every context-bound constructor goes through the
// pipe-drain default. It says nothing about whether a conditional caller runs.
// Tests are included: a fixture can otherwise hang the gate by the same route.
// This constructor rule scans tracked source only. Untracked scratch Go files
// are not inputs to this scan.
func TestEveryContextCommandUsesPipeDrainBound(t *testing.T) {
	root := repoRoot(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable: tracked-source hygiene requires a checkout")
	}
	if err := procexec.Command(t.Context(), git, "-C", root, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		t.Skip("not a git checkout: tracked-source hygiene does not apply to release tarballs")
	}
	cmd := procexec.Command(t.Context(), git, "-C", root, "ls-files", "-z", "--cached")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing tracked source: %v", err)
	}
	fset := token.NewFileSet()
	files, helperReferences := 0, 0
	for _, rel := range strings.Split(string(output), "\x00") {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("tracked Go source %s is not a readable regular file: %v", rel, err)
		}
		body, err := os.ReadFile(path) // #nosec G304 -- tracked repository source
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		f, err := parser.ParseFile(fset, rel, body, 0)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, pos := range contextCommandReferences(f) {
			if rel == "internal/procexec/command.go" && insideCommandHelper(f, pos) {
				helperReferences++
				continue
			}
			t.Errorf("%s: direct exec.CommandContext bypasses the pipe-drain bound; use procexec.Command (including in tests)", fset.Position(pos))
		}
	}
	if files < 100 || helperReferences != 1 {
		t.Fatalf("examined %d Go files and %d helper constructors, want a real source scan and exactly one helper", files, helperReferences)
	}
}

func insideCommandHelper(f *ast.File, pos token.Pos) bool {
	for _, declaration := range f.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "Command" && fn.Body != nil && fn.Body.Pos() <= pos && pos < fn.Body.End() {
			return true
		}
	}
	return false
}

// Import spellings are not policy: aliases, dot imports and constructor values
// all name the same raw constructor. Strings and comments name no Go object.
func contextCommandReferences(f *ast.File) []token.Pos {
	return commandReferences(f, map[string]bool{"CommandContext": true})
}

func commandReferences(f *ast.File, names map[string]bool) []token.Pos {
	aliases, dot := map[string]bool{}, false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "os/exec" {
			continue
		}
		name := "exec"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			dot = true
		} else {
			aliases[name] = true
		}
	}
	var positions []token.Pos
	selectors := map[*ast.Ident]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			selectors[sel.Sel] = true
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Obj == nil && aliases[pkg.Name] && names[sel.Sel.Name] {
				positions = append(positions, sel.Pos())
			}
		}
		return true
	})
	if dot {
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && names[id.Name] && id.Obj == nil && !selectors[id] {
				positions = append(positions, id.Pos())
			}
			return true
		})
	}
	return positions
}

func TestContextCommandGuardRecognizesImportSpellings(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         int
	}{
		{"ordinary", `package p; import "os/exec"; var c = exec.CommandContext`, 1},
		{"alias", `package p; import child "os/exec"; func f() { child.CommandContext(nil,"x") }`, 1},
		{"dot", `package p; import . "os/exec"; var c = CommandContext`, 1},
		{"text", `package p; var s = "exec.CommandContext" // exec.CommandContext`, 0},
		{"shadow", `package p; import child "os/exec"; var _ = child.Cmd{}; func f(child interface{CommandContext()}) { child.CommandContext() }`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "fixture_test.go", tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(contextCommandReferences(f)); got != tc.want {
				t.Fatalf("raw constructors = %d, want %d", got, tc.want)
			}
		})
	}
}
