// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestNativeQueueRouteCoalescesAndRearmsAcrossProcesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	argv := []string{binary, "queue", "--thread", "fixture-thread", "--message", Compose("question")}
	count := func() int {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, "pending.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rows []any
		if err = json.Unmarshal(b, &rows); err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	for range 4 {
		// Every call enters the actual shared production command route; all state
		// survives only in the stand-in app, as it does across a daemon restart.
		if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
			t.Fatal("setup: wake route failed")
		}
	}
	if got := count(); got != 1 {
		t.Fatalf("N events across independent calls queued %d entries, want 1", got)
	}
	if err := NoteQueueReconnect("fixture-thread", "replacement-app"); err != nil {
		t.Fatal(err)
	}
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) || count() != 1 {
		t.Fatal("reconnect duplicated an authoritative pending wake")
	}
	// The real observation must remain useful if the next probe is unavailable.
	t.Setenv("DIBS_QUEUE_PROBE_FAIL", "1")
	if out := RunCommandsWithOutcome(argv, nil, "worker", "", time.Second, time.Second); !out.Deferred || out.OK || count() != 1 {
		t.Fatal("unknown probe duplicated a wake retained on reconnect")
	}
	t.Setenv("DIBS_QUEUE_PROBE_FAIL", "")
	if err := os.WriteFile(filepath.Join(home, "pending.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
		t.Fatal("consumed wake did not re-arm")
	}
	if got := count(); got != 1 {
		t.Fatalf("after consumption queued %d", got)
	}
	methods, err := os.ReadFile(filepath.Join(home, "methods"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range strings.Fields(string(methods)) {
		if m != "initialize" && m != "initialized" && m != "thread/read" && m != "thread/queue/list" {
			t.Fatalf("observer used mutating method %s", m)
		}
	}
}

func TestQueueFallbackSurvivesRestart(t *testing.T) {
	if os.Getenv("DIBS_QUEUE_RESTART_HELPER") == "1" {
		argv := []string{os.Getenv("DIBS_QUEUE_FIXTURE"), "queue", "--thread", "fixture-thread", "--message", Compose("question")}
		if out := RunCommandsWithOutcome(argv, nil, "worker", "", time.Second, time.Second); !out.Deferred || out.OK {
			t.Fatalf("restart route did not defer on the recent receipt: %+v", out)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_QUEUE_PROBE_FAIL", "1")
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	t.Setenv("DIBS_QUEUE_FIXTURE", binary)
	argv := []string{binary, "queue", "--thread", "fixture-thread", "--message", Compose("question")}
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
		t.Fatal("first wake failed")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestQueueFallbackSurvivesRestart$")
	child.Env = append(os.Environ(), "DIBS_QUEUE_RESTART_HELPER=1")
	if b, err := child.CombinedOutput(); err != nil {
		t.Fatalf("fresh process route %v: %s", err, b)
	}
	count := func() int {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, "pending.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rows []any
		if err = json.Unmarshal(b, &rows); err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if count() != 1 {
		t.Fatal("restart queued a duplicate with unavailable observer")
	}
	st, err := os.Stat(receiptPath("fixture-thread"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions %o", st.Mode().Perm())
	}
	NoteQueuePrompt("fixture-thread", time.Now())
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) || count() != 2 {
		t.Fatal("prompt did not re-arm fallback")
	}
	if err = writeReceipt("fixture-thread", queueReceipt{QueuedAt: time.Now().Add(-30 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) || count() != 3 {
		t.Fatal("TTL did not re-arm fallback")
	}
}

func TestUnknownQueueObservationDoesNotClaimAStaleReceipt(t *testing.T) {
	binary := staleQueueFixture(t)
	for _, tc := range []struct {
		name, reportedThread   string
		age                    time.Duration
		newTurn, wantAdmission bool
	}{
		{name: "same-thread-turned", age: 2 * time.Second, newTurn: true, wantAdmission: true},
		{name: "expired-inference", age: 30 * time.Second, wantAdmission: true},
		{name: "ambiguous-recent-receipt", age: 2 * time.Second},
		{name: "different-thread", age: 2 * time.Second, newTurn: true, reportedThread: "other-thread"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := staleQueueHome(t)
			const thread = "receipt-thread"
			argv := []string{binary, "queue", "--thread", thread, "--message", Compose("notify")}
			queuedAt := time.Now().UTC().Add(-tc.age).Truncate(time.Second)
			turnAt := queuedAt.Add(-time.Second)
			if tc.newTurn {
				turnAt = queuedAt
			}
			t.Setenv("DIBS_TEST_QUEUE_THREAD_SOURCE", "appServer")
			t.Setenv("DIBS_TEST_QUEUE_LIST_UNKNOWN", "1")
			t.Setenv("DIBS_TEST_QUEUE_LAST_TURN_STARTED", strconv.FormatInt(turnAt.Unix(), 10))
			t.Setenv("DIBS_TEST_QUEUE_SOURCE_THREAD", tc.reportedThread)
			if err := writeReceipt(thread, queueReceipt{QueuedAt: queuedAt}); err != nil {
				t.Fatal("setup: write receipt:", err)
			}
			var seen QueueObservation
			out := RunCommandsObserved(argv, nil, "worker", "", time.Second, time.Second, func(o QueueObservation) {
				seen = o
			})
			if tc.wantAdmission {
				if !out.OK || seen.Admission != "accepted" {
					t.Fatalf("receipt suppressed an admissible wake: outcome=%+v observation=%+v", out, seen)
				}
				if n := len(staleQueueRows(t, home)); n != 1 {
					t.Fatalf("admitted %d items, want one", n)
				}
				return
			}
			if out.OK || out.NoRetry || seen.Admission != "deferred" || seen.Pending != "unknown" || !seen.ReceiptAt.Equal(queuedAt) {
				t.Fatalf("inferred receipt did not defer honestly: outcome=%+v observation=%+v", out, seen)
			}
			if _, err := os.Stat(filepath.Join(home, "pending.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deferred route executed the queue command: %v", err)
			}
		})
	}
}

func TestQueueProbeTimeoutAndChangedShapeAreUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_QUEUE_PROBE_STALL", "1")
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	// The first measured launch can miss readiness even after preparation.
	t.Setenv("DIBS_TEST_QUEUE_COLD_EXEC", filepath.Join(home, "cold-probe"))
	pidPath := filepath.Join(home, "probe.pid")
	checkStalledQueueProbe(t, pidPath, "queue probe", func(_ time.Time) {
		pending, known := observeQueue(binary, "fixture-thread")
		if pending || known {
			t.Fatal("a timeout was guessed to be an observed queue")
		}
	})
	checkStalledQueueProbe(t, pidPath, "contact probe", func(start time.Time) {
		if got := ObserveQueueContact(t.Context(), binary, "fixture-thread", start, start.Add(-time.Minute)); got != QueueContactWait {
			t.Fatalf("contact probe guessed at thread activity after a timeout: %v", got)
		}
	})
	for _, s := range []string{`{}`, `{"data":null,"nextCursor":null}`, `{"data":[],"nextCursor":4}`, `{"data":[{}],"nextCursor":null}`} {
		if _, known, _ := queuePage([]byte(s)); known {
			t.Fatalf("changed shape accepted: %s", s)
		}
	}
}

func checkStalledQueueProbe(t *testing.T, pidPath, name string, probe func(time.Time)) {
	t.Helper()
	const attempts = 3
	for range attempts {
		start := time.Now()
		probe(start)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("%s did not kill/reap at its deadline: %v", name, elapsed)
		}
		probePID, err := os.ReadFile(pidPath)
		if errors.Is(err, os.ErrNotExist) {
			// The probe timed out before the fixture reached the stall. The
			// timeout still had to return unknown and meet the same deadline.
			continue
		}
		if err != nil {
			t.Fatalf("setup: read %s readiness: %v", name, err)
		}
		assertQueueProbeReaped(t, name, probePID)
		if err := os.Remove(pidPath); err != nil {
			t.Fatalf("setup: clear %s readiness: %v", name, err)
		}
		return
	}
	t.Fatalf("setup: %s did not reach the stall in %d attempts", name, attempts)
}

func assertQueueProbeReaped(t *testing.T, name string, probePID []byte) {
	t.Helper()
	pid, err := strconv.Atoi(string(probePID))
	if err != nil || pid <= 0 {
		t.Fatalf("setup: invalid %s PID %q", name, probePID)
	}
	if runtime.GOOS != "windows" {
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = process.Release() }()
		err = process.Signal(syscall.Signal(0))
		if err == nil {
			_ = process.Kill()
			t.Fatalf("timed-out %s subprocess remained in the process table", name)
		}
		if !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("could not verify %s subprocess cleanup: %v", name, err)
		}
	}
}
