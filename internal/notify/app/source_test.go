// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A future include must enter the same catalog as the existing prompt include,
// without adding its name to a build tool or any of the native fixtures.
func TestReadSourceIncludesEveryFragmentInStableOrder(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"notify_darwin.swift":     "main",
		"prompt_window.swift.inc": "prompt",
		"z_future.inc":            "future",
		"a_first.inc":             "first",
		"not-shipped.swift":       "exclude",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "main\nfirst\nprompt\nfuture\n" {
		t.Fatalf("shipped source composition omitted or reordered a fragment: %q", got)
	}
}
