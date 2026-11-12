// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObserverSeesExecutedCommand(t *testing.T) {
	receipt := filepath.Join(t.TempDir(), "executed")
	calls := 0
	restore := ObserveCommands(func(name string) {
		calls++
		if name != os.Args[0] {
			t.Errorf("observer named a different command: %q", name)
		}
	})
	t.Cleanup(restore)
	cmd := Command(t.Context(), os.Args[0], "-test.run=^TestCommandObserverChild$")
	cmd.Env = append(os.Environ(), "DIBS_TEST_OBSERVER_RECEIPT="+receipt)
	if err := cmd.Run(); err != nil {
		t.Fatal("setup: observed command did not run:", err)
	}
	raw, err := os.ReadFile(receipt)
	if err != nil || string(raw) != "executed" {
		t.Fatalf("setup: command did not produce its execution receipt: %q %v", raw, err)
	}
	if calls != 1 {
		t.Fatalf("real executed command produced %d observer events, want1", calls)
	}
}

func TestCommandObserverChild(t *testing.T) {
	if path := os.Getenv("DIBS_TEST_OBSERVER_RECEIPT"); path != "" {
		if err := os.WriteFile(path, []byte("executed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArgumentObserverCannotRewriteCommand(t *testing.T) {
	const arg = "-test.run=^TestCommandObserverChild$"
	seen := false
	restore := ObserveCommandArgs(func(name string, args []string) {
		seen = true
		if name != os.Args[0] || len(args) != 1 || args[0] != arg {
			t.Fatalf("observer missed the actual constructor arguments: %q %q", name, args)
		}
		args[0] = "rewritten"
	})
	defer restore()
	cmd := Command(t.Context(), os.Args[0], arg)
	if !seen || len(cmd.Args) != 2 || cmd.Args[1] != arg {
		t.Fatalf("observer changed command execution: %q", cmd.Args)
	}
}
