// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestNativeQueueFallbackUsesTheSameAdmissionDoor(t *testing.T) {
	root, err := os.MkdirTemp("", "dibs-fallback-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("failed fallback fixture retained at %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	home := filepath.Join(root, "home")
	board := filepath.Join(root, "board")
	for _, dir := range []string{home, board} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", board)
	t.Setenv("DIBS_TEST_QUEUE_EVIDENCE", root)
	binary := filepath.Join(root, "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	primary := []string{binary, "primary"}
	fallback := []string{binary, "queue", "--thread", "fallback-thread", "--message", Compose("question")}
	for range 2 {
		started := time.Now()
		if !RunCommands(primary, fallback, "worker", "", time.Second, time.Second) {
			logFallbackEvidence(t, root, started)
			t.Fatal("fallback did not succeed after the primary refusal; see stage timings and stderr above")
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

func logFallbackEvidence(t *testing.T, root string, started time.Time) {
	t.Helper()
	t.Logf("fallback failed after %s; command budget 1s, drain bound 1s", time.Since(started))
	stderr, err := os.ReadFile(filepath.Join(root, "queue.stderr"))
	if err != nil {
		t.Logf("fallback stderr unavailable: %v (the fixture may not have entered main)", err)
	} else {
		t.Logf("fallback stderr: %q", stderr)
	}
	f, err := os.Open(filepath.Join(root, "stages.jsonl"))
	if err != nil {
		t.Logf("fallback stage evidence unavailable: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	previous := started
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var stage struct {
			At    int64  `json:"at_ns"`
			Mode  string `json:"mode"`
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &stage); err != nil {
			t.Logf("bad fixture stage: %v", err)
			continue
		}
		at := time.Unix(0, stage.At)
		if at.Before(started) {
			continue
		}
		t.Logf("%s %s: +%s since prior stage, %s total", stage.Mode, stage.Phase, at.Sub(previous), at.Sub(started))
		previous = at
	}
	if err := scanner.Err(); err != nil {
		t.Logf("reading fixture stages: %v", err)
	}
	t.Logf("last stage to failure: %s", time.Since(previous))
}

// The actual command route keeps its one-second budget. Only the fixture's
// first exec is slow, so deleting preparation must fail the existing real-door
// assertions rather than passing because this machine happens to be warm.
func TestNativeQueuePreparesColdExecutableBeforeDeadline(t *testing.T) {
	t.Setenv("DIBS_TEST_QUEUE_COLD_EXEC", filepath.Join(t.TempDir(), "cold-first-exec"))
	TestNativeQueueFallbackUsesTheSameAdmissionDoor(t)
}
