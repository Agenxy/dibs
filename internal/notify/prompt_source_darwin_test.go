// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"os"
	"path/filepath"
	"testing"

	notifyapp "github.com/agenxy/dibs/internal/notify/app"
)

// Match tools/appbundle's one-translation-unit build. Tests replace only
// native notification contact under compile-time fixture flags.
func nativePromptSource(t *testing.T) string {
	t.Helper()
	source, err := notifyapp.ReadSource("app")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "notify.swift")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
