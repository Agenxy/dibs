// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestClaimWriteFailureUnlocksAnInheritedDescription(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the real host registry
	original := writeDaemonEntry
	t.Cleanup(func() { writeDaemonEntry = original })
	injected := errors.New("fixture registry write failure")
	duplicate := -1
	writeDaemonEntry = func(f *os.File, _ []byte, _ int64) (int, error) {
		var err error
		duplicate, err = syscall.Dup(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		syscall.CloseOnExec(duplicate)
		return 0, injected
	}
	pid := os.Getpid()
	release, err := Claim(Daemon{PID: pid, Dir: t.TempDir()}, true)
	if release != nil {
		t.Cleanup(release)
	}
	if duplicate >= 0 {
		defer func() { _ = syscall.Close(duplicate) }()
	}
	if !errors.Is(err, injected) || duplicate < 0 {
		t.Fatalf("setup: real Claim did not reach the injected write: release=%v err=%v dup=%d", release != nil, err, duplicate)
	}
	path := filepath.Join(RunRegistryDir(), strconv.Itoa(pid)+".json")
	probe, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := LockExclusive(probe, false); err != nil {
		t.Fatalf("failed Claim left the registry lock held by an inherited description: %v", err)
	}
	Unlock(probe)
}
