// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorNamesRemovedWakeCooldown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	if err := os.WriteFile(path, []byte("[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\ncooldown = \"90s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	checkWakeRoutes(dir, nil, hubHosts{}, func(string) {}, func(message, fix string) {
		warnings = append(warnings, message+" "+fix)
	})
	got := strings.Join(warnings, "\n")
	for _, want := range []string{path, "[wake.exec.codex] cooldown", "removed", "Remove"} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor did not name the obsolete setting and repair %q: %s", want, got)
		}
	}
}
