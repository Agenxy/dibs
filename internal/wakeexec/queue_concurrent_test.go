package wakeexec

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeQueueIndependentWritersCoalesce(t *testing.T) {
	if tag := os.Getenv("DIBS_QUEUE_RACE_HELPER"); tag != "" {
		if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "caller-ready-"+tag), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		argv := []string{os.Getenv("DIBS_QUEUE_FIXTURE"), "queue", "--thread", "race-thread", "--message", Compose("question")}
		if !RunCommands(argv, nil, "worker", "", 8*time.Second, time.Second) {
			t.Fatal("real queue command door failed")
		}
		return
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build %v: %s", err, b)
	}
	t.Setenv("DIBS_QUEUE_FIXTURE", binary)
	start := func(tag string) (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeQueueIndependentWritersCoalesce$")
		cmd.Env = append(os.Environ(), "DIBS_QUEUE_RACE_HELPER="+tag)
		out := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		return cmd, out
	}
	waitFile := func(name string, limit time.Duration) bool {
		t.Helper()
		deadline := time.NewTimer(limit)
		defer deadline.Stop()
		poll := time.NewTicker(5 * time.Millisecond)
		defer poll.Stop()
		for {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				return true
			}
			select {
			case <-deadline.C:
				return false
			case <-poll.C:
			}
		}
	}
	a, outA := start("a")
	if !waitFile("command-ready-a", 5*time.Second) {
		t.Fatal("setup: first writer never reached its queue command")
	}
	b, outB := start("b")
	if !waitFile("caller-ready-b", 5*time.Second) {
		t.Fatal("setup: independent second caller never started")
	}
	// The first command holds no queue item yet. A second process must wait
	// for that admission, then observe its item, rather than observe empty too.
	_ = waitFile("command-ready-b", time.Second)
	if err := os.WriteFile(filepath.Join(home, "command-release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatalf("first command door: %v: %s", err, outA.Bytes())
	}
	if err := b.Wait(); err != nil {
		t.Fatalf("second command door: %v: %s", err, outB.Bytes())
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
		t.Fatalf("two independent real command callers queued %d entries, want 1", len(rows))
	}
}
