// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package liveness

import (
	"errors"
	"os"
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
	restore := procexec.ObserveCommandArgs(func(name string, args []string) {
		commands.Add(1)
		t.Logf("zombie observation prepared command: %s %q", name, args)
	})
	defer restore()
	_, _ = processTimes(pid)
	if files := openFiles(pid); len(files) != 0 {
		t.Errorf("unreaped exited child held files: %v", files)
	}
	if got := commands.Load(); got != 0 {
		t.Errorf("zombie CPU/descriptor observation prepared %d child commands", got)
	}
}

func TestZombieFallbacksDoNotFork(t *testing.T) {
	for _, kind := range []string{"cpu", "descriptors"} {
		t.Run(kind, func(t *testing.T) {
			pid := testexec.Zombie(t)
			old := nativeOpenFiles
			t.Cleanup(func() { nativeOpenFiles = old })
			// Drive the real fallback entrance after the native observation has
			// failed, even on systems where the zombie's fd directory is readable.
			nativeOpenFiles = func(int, int) ([]string, error) {
				return nil, errors.New("native descriptor evidence unavailable")
			}
			var commands atomic.Int32
			restore := procexec.ObserveCommandArgs(func(name string, args []string) {
				commands.Add(1)
				t.Logf("exited process fallback prepared: %s %q", name, args)
			})
			defer restore()
			if kind == "cpu" {
				_, _ = psTimes(pid)
			} else {
				_ = openFiles(pid)
			}
			if got := commands.Load(); got != 0 {
				t.Errorf("exited process %s fallback prepared %d commands", kind, got)
			}
		})
	}
}

func TestLiveCPUFallbackStillPreparesPS(t *testing.T) {
	var commands atomic.Int32
	restore := procexec.ObserveCommands(func(name string) {
		if name == "ps" {
			commands.Add(1)
		}
	})
	defer restore()
	_, _ = psTimes(os.Getpid())
	if got := commands.Load(); got != 1 {
		t.Fatalf("live CPU fallback prepared %d ps commands, want 1", got)
	}
}
