// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hygiene

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// This is a static property of the daemon's constructors. Conditional runtime
// behaviour is separately exercised by the engine's real idle-observation test.
// CLI service-management commands and stand-alone test fixtures are outside the
// daemon runtime; every production library used by dibd is inside this scan.
func TestDaemonCommandsUseObservedConstructor(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	files, helper := 0, 0
	for _, dir := range []string{"internal", "cmd/dibd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			files++
			for _, pos := range commandReferences(file, map[string]bool{"Command": true, "CommandContext": true}) {
				if path == filepath.Join(root, "internal/procexec/command.go") && insideCommandHelper(file, pos) {
					helper++
					continue
				}
				t.Errorf("%s: daemon command bypasses the observed constructor; use procexec.Command", fset.Position(pos))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 100 || helper != 1 {
		t.Fatalf("source scan saw %d files and %d helper constructors", files, helper)
	}
}

func TestDaemonCommandGuardRecognizesBothRawConstructors(t *testing.T) {
	for _, source := range []string{
		`package p; import "os/exec"; var a=exec.Command; var b=exec.CommandContext`,
		`package p; import child "os/exec"; var a=child.Command; var b=child.CommandContext`,
		`package p; import . "os/exec"; var a=Command; var b=CommandContext`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(commandReferences(file, map[string]bool{"Command": true, "CommandContext": true})); got != 2 {
			t.Fatalf("raw constructors=%d, want2", got)
		}
	}
}
