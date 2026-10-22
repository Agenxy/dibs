// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Enter the existing resolver used by CLI/daemon/bridge, not a parser called
// only from the test. Rewriting the file proves selection is never cached.
func TestServiceDirectoryIsSharedAndReadAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	file := filepath.Join(home, "Library", "Application Support", "org.agenxy.dibs", "service.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"original-board", "moved-board"} {
		dir := filepath.Join(home, name)
		if err := os.WriteFile(file, []byte(fmt.Sprintf("version = 1\ndata_dir = %q\n", dir)), 0o600); err != nil {
			t.Fatal(err)
		}
		got, inherited := Resolve()
		if got != dir || inherited != "" || DataDir() != dir {
			t.Errorf("shared service selection = %q/%q, DataDir %q; want %q", got, inherited, DataDir(), dir)
		}
	}
	t.Setenv("DIBS_DIR", filepath.Join(home, "explicit"))
	if DataDir() != filepath.Join(home, "explicit") {
		t.Fatal("service pin overrode explicit DIBS_DIR")
	}
}
