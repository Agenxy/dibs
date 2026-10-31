// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"os"
	"path/filepath"
	"testing"
)

// Match tools/appbundle's one-translation-unit build. Tests replace only
// native notification contact under compile-time fixture flags.
func nativePromptSource(t *testing.T) string {
	t.Helper()
	main, err := os.ReadFile("app/notify_darwin.swift")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile("app/prompt_window.swift.inc")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "notify.swift")
	if err := os.WriteFile(path, append(append(main, '\n'), prompt...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
