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
	for _, joined := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "joined"}[joined], func(t *testing.T) {
			testDoctorRemovedWakeCooldown(t, joined)
		})
	}
}

func testDoctorRemovedWakeCooldown(t *testing.T, joined bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dibs.toml")
	if err := os.WriteFile(path, []byte("[wake.exec.codex]\nargv = [\"codex\", \"queue\"]\ncooldown = \"90s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	warn := func(message, fix string) {
		warnings = append(warnings, message+" "+fix)
	}
	if joined {
		checkJoinedWakeRoutes(dir, &boardView{Node: "other-fixture-node"}, hubHosts{}, func(string) {}, warn)
	} else {
		checkWakeRoutes(dir, nil, hubHosts{}, func(string) {}, warn)
	}
	got := strings.Join(warnings, "\n")
	for _, want := range []string{path + ":3", "[wake.exec.codex] cooldown", "removed", "delete the line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor did not name the obsolete setting and repair %q: %s", want, got)
		}
	}
}
