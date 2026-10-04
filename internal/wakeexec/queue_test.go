package wakeexec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeQueueRouteCoalescesAndRearmsAcrossProcesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v: %s", err, b)
	}
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
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) || count() != 1 {
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
		if m != "initialize" && m != "initialized" && m != "thread/queue/list" {
			t.Fatalf("observer used mutating method %s", m)
		}
	}
}

func TestQueueFallbackSurvivesRestart(t *testing.T) {
	if os.Getenv("DIBS_QUEUE_RESTART_HELPER") == "1" {
		argv := []string{os.Getenv("DIBS_QUEUE_FIXTURE"), "queue", "--thread", "fixture-thread", "--message", Compose("question")}
		if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
			t.Fatal("restart route failed")
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
	if err = writeReceipt("fixture-thread", queueReceipt{QueuedAt: time.Now().Add(-3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) || count() != 3 {
		t.Fatal("TTL did not re-arm fallback")
	}
}

func TestQueueProbeTimeoutAndChangedShapeAreUnknown(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("DIBS_QUEUE_PROBE_STALL", "1")
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build %v: %s", err, b)
	}
	start := time.Now()
	pending, known := observeQueue(binary, "fixture-thread")
	if pending || known {
		t.Fatal("a timeout was guessed to be an observed queue")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("probe did not kill/reap at its deadline: %v", elapsed)
	}
	for _, s := range []string{`{}`, `{"data":null,"nextCursor":null}`, `{"data":[],"nextCursor":4}`, `{"data":[{}],"nextCursor":null}`} {
		if _, known, _ := queuePage([]byte(s)); known {
			t.Fatalf("changed shape accepted: %s", s)
		}
	}
}
