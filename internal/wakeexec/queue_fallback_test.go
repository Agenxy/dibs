// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeQueueFallbackUsesTheSameAdmissionDoor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build %v: %s", err, b)
	}
	primary := []string{binary, "primary"}
	fallback := []string{binary, "queue", "--thread", "fallback-thread", "--message", Compose("question")}
	for range 2 {
		if !RunCommands(primary, fallback, "worker", "", time.Second, time.Second) {
			t.Fatal("setup: primary refusal did not enter the queue fallback")
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []any
	if err = json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("two fallback calls queued %d entries, want 1", len(rows))
	}
}
