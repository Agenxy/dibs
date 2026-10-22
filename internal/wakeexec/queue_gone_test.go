// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestMissingRolloutIsGoneThroughRunCommandsObserved(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, tc := range []struct {
		name, errorText string
		gone            bool
	}{
		{"missing", "thread/queue/add failed: failed to read thread: invalid thread-store request: no rollout found for thread id missing-thread (code -32603)", true},
		{"missing-after-open-output", "previous active writer\nthread/queue/add failed: no rollout found for thread id missing-thread (code -32603)", true},
		{"temporary", "thread/queue/add failed: thread store temporarily unavailable (code -32603)", false},
		{"different-thread", "thread/queue/add failed: no rollout found for thread id another-thread (code -32603)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("DIBS_DIR", t.TempDir())
			t.Setenv("DIBS_TEST_QUEUE_ERROR_THREAD", "missing-thread")
			t.Setenv("DIBS_TEST_QUEUE_ERROR", tc.errorText)
			var observation QueueObservation
			queue := []string{binary, "queue", "--thread", "missing-thread", "--message", Compose("request")}
			if RunCommandsObserved(queue, []string{binary, "fallback-marker"}, "worker", "", 5*time.Second, time.Second,
				func(o QueueObservation) { observation = o }) {
				t.Fatal("failed queue command was reported as delivery")
			}
			attempts, err := os.ReadFile(filepath.Join(home, "queue-attempts"))
			if err != nil || strings.TrimSpace(string(attempts)) != "missing-thread" || observation.Admission != "failed" {
				t.Fatalf("setup: expected one real failed queue attempt: %q %v %+v", attempts, err, observation)
			}
			if _, err := os.Stat(filepath.Join(home, "fallback-ran")); !os.IsNotExist(err) {
				t.Fatal("missing/temporary thread failure incorrectly ran the open-thread fallback")
			}
			// Marshal the public observation so this guard also compiles against
			// the pre-fix API, where absence must fail at runtime, not compile time.
			data, err := json.Marshal(observation)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if gone := fields["ThreadState"] == "gone"; gone != tc.gone {
				t.Fatalf("real queue error gone=%v, want %v: %s", gone, tc.gone, data)
			}
		})
	}
}
