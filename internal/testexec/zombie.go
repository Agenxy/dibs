// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package testexec

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

// Zombie starts a real short-lived child but leaves Wait to cleanup. Tests
// observe the kernel's exit state independently of procinfo's interpretation.
func Zombie(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cmd := procexec.Command(ctx, "/usr/bin/true")
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal("setup child:", err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	AwaitZombie(t, cmd.Process.Pid)
	return cmd.Process.Pid
}

// AwaitZombie uses the raw kernel state, never Start/Snapshot/SameProcess, so
// a changed product answer cannot make a broken fixture look like an exit.
func AwaitZombie(t *testing.T, pid int) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(until) {
		var zombie bool
		zombie, last = kernelZombie(pid)
		if last == nil && zombie {
			return
		}
		<-time.After(time.Millisecond)
	}
	t.Fatalf("setup: child %d never reached unreaped kernel exit state: %v", pid, last)
}
