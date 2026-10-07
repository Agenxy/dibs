// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeQueueContentionIsQuietAndRunsNoFallback(t *testing.T) {
	if tag := os.Getenv("DIBS_QUEUE_RACE_HELPER"); tag != "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
		argv := []string{
			os.Getenv("DIBS_QUEUE_FIXTURE"), "queue", "--thread", "race-thread", "--message", Compose("question"),
		}
		limit := 8 * time.Second
		if tag == "b" {
			limit = 100 * time.Millisecond
		}
		ok := RunCommands(argv, []string{argv[0], "fallback-marker"}, "worker", "", limit, time.Second)
		if ok != (tag == "a") {
			t.Fatalf("command outcome for %s was %v", tag, ok)
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
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeQueueContentionIsQuietAndRunsNoFallback$")
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
	a, outA := start("a")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(filepath.Join(home, "command-ready-a")); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("setup: first process never entered its actual queue command")
		case <-poll.C:
		}
	}
	b, outB := start("b")
	errB := b.Wait()
	_, queuedByB := os.Stat(filepath.Join(home, "command-ready-b"))
	_, fallbackByB := os.Stat(filepath.Join(home, "fallback-ran"))
	if err := os.WriteFile(filepath.Join(home, "command-release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatalf("first process failed: %v: %s", err, outA.Bytes())
	}
	if errB != nil {
		t.Fatalf("second process failed: %v: %s", errB, outB.Bytes())
	}
	if !os.IsNotExist(queuedByB) || !os.IsNotExist(fallbackByB) {
		t.Fatal("contended second process ran a queue or fallback command")
	}
	output := outB.String()
	if strings.Contains(output, "level=WARN") ||
		!strings.Contains(output, "level=DEBUG msg=\"app queue writer is busy") {
		t.Fatalf("contention was not diagnosed quietly as a busy writer: %s", output)
	}
}
