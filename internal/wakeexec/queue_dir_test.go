// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
)

// 2026-10-22: macOS stopped the launchd daemon reaching ~/Desktop, and every
// `codex queue` wake to an agent working there exited 1, because the queue
// route ran in the agent's directory. It addresses a thread by id and needs
// no project directory, so it must not depend on reaching one.
func TestAQueueWakeDoesNotRunInTheAgentsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	record := filepath.Join(t.TempDir(), "cwd")
	t.Setenv("DIBS_TEST_QUEUE_RECORD_CWD", record)
	agentDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("setup:", err)
	}

	queue := []string{binary, "queue", "--thread", "dir-thread", "--message", Compose("question")}
	if !RunCommands(queue, nil, "worker", agentDir, 5*time.Second, time.Second) {
		t.Fatal("setup: the queue command did not run, so this proves nothing about where")
	}
	ran, err := os.ReadFile(record)
	if err != nil {
		t.Fatal("setup: the fixture did not record its directory:", err)
	}
	if resolved, _ := filepath.EvalSymlinks(string(ran)); resolved == agentDir {
		t.Fatalf("the queue wake ran in the agent's directory %s; it needs none, "+
			"and a directory the daemon cannot reach fails every wake", agentDir)
	}
}
