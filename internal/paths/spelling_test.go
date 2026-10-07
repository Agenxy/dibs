// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCanonicalNativeCaseSpelling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native F_GETPATH spelling is a Darwin contract")
	}
	root := filepath.Join(t.TempDir(), "Supgang")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "SupGang")
	a, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("fixture volume is case-sensitive; spellings are distinct")
	}
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("setup did not prove one object: %v", err)
	}
	for _, suffix := range []string{"", "new/file.go"} {
		if got, want := Canonical(filepath.Join(alias, suffix)), Canonical(filepath.Join(root, suffix)); got != want {
			t.Errorf("same native object has two spellings: %q != %q", got, want)
		}
	}
	if Portable(alias) != alias {
		t.Fatal("remote paths were resolved against this filesystem")
	}
}

func TestCanonicalDistinctCaseDirectories(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "role"), filepath.Join(dir, "Role")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(b, 0o700); os.IsExist(err) {
		t.Skip("fixture volume is case-insensitive; use native alias guard")
	} else if err != nil {
		t.Fatal(err)
	}
	if Canonical(a) == Canonical(b) {
		t.Fatal("distinct directories were merged by case spelling")
	}
}
