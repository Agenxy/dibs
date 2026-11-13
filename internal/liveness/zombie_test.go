// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package liveness

import (
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestZombieObservationDoesNotForkAndKeepsExistenceMeaning(t *testing.T) {
	pid := testexec.Zombie(t)
	// The coarse public prober deliberately means PID existence, including
	// permission-denied peers. This identity fix must not rewrite its contract.
	if !New().Alive(pid) {
		t.Fatal("PID-existence probe changed meaning for an unreaped child")
	}
	var commands atomic.Int32
	restore := procexec.ObserveCommands(func(string) { commands.Add(1) })
	defer restore()
	_, _ = processTimes(pid)
	if files := openFiles(pid); len(files) != 0 {
		t.Errorf("unreaped exited child held files: %v", files)
	}
	if got := commands.Load(); got != 0 {
		t.Errorf("zombie CPU/descriptor observation prepared %d child commands", got)
	}
}
