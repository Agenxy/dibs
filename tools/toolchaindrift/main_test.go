// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerdictPassesOnlyWhenEverythingIsUpstreamLatest(t *testing.T) {
	lines, behind := verdict(nil, []string{"2026.10.23"}, "2026.10.23")
	if behind || !strings.HasPrefix(lines[0], "current:") {
		t.Fatalf("current toolchain reported as behind: %v", lines)
	}
	stale := map[string]outdatedTool{"go": {Current: "1.26.10", Latest: "1.27.3"}}
	lines, behind = verdict(stale, []string{"2026.10.23"}, "2026.10.23")
	if !behind || !strings.Contains(lines[0], "go 1.26.10, upstream 1.27.3") {
		t.Fatalf("a stale mise.toml pin passed: %v", lines)
	}
	lines, behind = verdict(nil, []string{"2026.7.7"}, "2026.10.23")
	if !behind || !strings.Contains(lines[0], "mise 2026.7.7, upstream 2026.10.23") {
		t.Fatalf("a stale workflow mise version passed: %v", lines)
	}
}

// The pattern is the guard's only view of the workflows, so it is measured
// against this repository's real files rather than a fixture that could drift.
func TestEveryWorkflowMisePinIsSeen(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	versions, err := workflowMiseVersions(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pins := 0
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- this repo's own workflows
		if err != nil {
			t.Fatal(err)
		}
		pins += strings.Count(string(raw), "jdx/mise-action@")
	}
	if pins == 0 || len(versions) == 0 {
		t.Fatalf("saw %d mise-action uses and versions %v", pins, versions)
	}
}
