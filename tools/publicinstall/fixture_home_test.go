// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In a Linux child the receipts are a bind mount Dibs will not keep a profile
// under, so each leg's home has to be somewhere else, private, and its daemon
// log has to come back to the receipts.
func TestALinuxChildKeepsItsProfilesOutOfTheReceiptMount(t *testing.T) {
	root := t.TempDir()
	r := &runner{root: root, privateHomes: true}
	home, err := r.fixtureHome("archive")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if strings.HasPrefix(home, root) {
		t.Fatalf("child home %s is under the receipts %s", home, root)
	}
	if info, err := os.Stat(home); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("child home not private: %v %v", info.Mode(), err)
	}
	if err := os.WriteFile(filepath.Join(home, "daemon.log"), []byte("up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.keepDaemonLog(home, "archive")
	if raw, err := os.ReadFile(filepath.Join(root, "archive-daemon.log")); err != nil || string(raw) != "up\n" {
		t.Fatalf("daemon log not kept in the receipts: %q %v", raw, err)
	}

	native := &runner{root: root}
	home, err = native.fixtureHome("go")
	if err != nil || home != filepath.Join(root, "go-home") {
		t.Fatalf("native home %s, %v: want beside the receipts", home, err)
	}
}
