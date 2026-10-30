// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDaemonReplayPreflightRefusesNativeDirectoryACLWithoutDeletingIt(t *testing.T) {
	dir := qShortDir(t)
	addr, stop := qStartBoard(t, dir)
	stop()
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow add_file,search", dir).CombinedOutput(); err != nil {
		t.Fatalf("setup: native directory ACL: %v %s", err, out)
	}
	out, err := qDirectoryChild(t, dir, addr, true)
	if err == nil || !strings.Contains(string(out), "ls -le") || !strings.Contains(string(out), dir) {
		t.Fatalf("real replay preflight did not refuse unsafe ACL state: %v %s", err, out)
	}
	listing, err := exec.Command("/bin/ls", "-led", dir).CombinedOutput()
	if err != nil || !strings.Contains(string(listing), "everyone allow add_file,search") {
		t.Fatalf("refusal changed the ACL: %v %s", err, listing)
	}
}
