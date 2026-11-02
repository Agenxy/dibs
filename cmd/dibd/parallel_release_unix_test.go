// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// Enter through the real claim and its returned release, retaining the same
// open-file description a forked child holds before exec. Closing the parent's
// descriptor alone must not leave the next daemon locked out.
func TestHostSlotReleaseUnlocksAnInheritedDescription(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the real host registry
	dir := t.TempDir()
	release, err := claimHostSlot("127.0.0.1:4777", "", dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	duplicate := duplicateBoardLock(t, filepath.Join(dir, "lock"))
	defer func() { _ = syscall.Close(duplicate) }()
	probe, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := paths.LockExclusive(probe, false); !paths.LockHeldElsewhere(err) {
		t.Fatalf("setup: real board lock was not held: %v", err)
	}
	release()
	if err := paths.LockExclusive(probe, false); err != nil {
		t.Fatalf("released board lock is still held by the inherited description: %v", err)
	}
	paths.Unlock(probe)
}

// Find the descriptor opened by claimHostSlot, rather than introducing a
// production setter or calling a disconnected lock-release helper by hand.
func duplicateBoardLock(t *testing.T, path string) int {
	t.Helper()
	var wanted syscall.Stat_t
	if err := syscall.Stat(path, &wanted); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var stat syscall.Stat_t
		if syscall.Fstat(fd, &stat) != nil || stat.Dev != wanted.Dev || stat.Ino != wanted.Ino {
			continue
		}
		duplicate, err := syscall.Dup(fd)
		if err != nil {
			t.Fatal(err)
		}
		syscall.CloseOnExec(duplicate)
		return duplicate
	}
	t.Fatal("setup: real claim did not retain its board lock descriptor")
	return -1
}
