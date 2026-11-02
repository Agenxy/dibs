// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Prepare one read-only image before parallel cases start. Private hard links
// retain the prepared vnode without sharing any writable fixture state.
// Lifecycle cases which replace code still use writeNativeFixtureHelper.
func prepareReadOnlyNativeFixture(t *testing.T, prepare func(*testing.T, *exec.Cmd)) string {
	t.Helper()
	image, err := serviceFixtureImage(t)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cliIPCProfile(t), "native-fixture")
	if err := os.WriteFile(path, image, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	prepare(t, exec.Command(path, "--dibs-test-prepare")) // #nosec G204 -- private external double
	return path
}

func linkReadOnlyNativeFixture(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
}

// Called by the actual contract after installing every role, independently of
// the installer. Replacing a link with a fresh copy must fail this guard.
func requireReadOnlyNativeAlias(t *testing.T, source, alias string) {
	t.Helper()
	prepared, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Mode().IsRegular() || !linked.Mode().IsRegular() || !os.SameFile(prepared, linked) {
		t.Fatal("native alias does not share the prepared inode")
	}
	// Check the kernel's owner-write decision, not just the mode spelling.
	// Open without truncation so a failing control cannot corrupt code bytes.
	writable, err := os.OpenFile(alias, os.O_WRONLY, 0)
	if err == nil {
		_ = writable.Close()
		t.Fatal("read-only native alias permits owner writes")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unexpected owner-write refusal: %v", err)
	}
	if linked.Mode().Perm() != 0o500 {
		t.Fatalf("native alias mode is %o, want 500", linked.Mode().Perm())
	}
}
