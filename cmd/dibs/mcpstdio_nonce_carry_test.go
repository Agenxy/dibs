// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"strconv"
	"testing"
)

// A bridge that learns its harness session keeps the nonce it was handed
// while it could only name its own process.
//
// Measured on this board's own architect seat on 2026-11-20: the bridge had
// filed its nonce under `bridge-process:<pid>:<start>` because a stale
// CLAUDE_PID hid its sidecar. Once that was fixed, the same bridge reported
// `harness-session:<id>`, found an empty slot, minted a fresh nonce, and the
// register was refused with E_SESSION_TAKEN. A dormant agent in the same
// position forks a sibling instead.
func TestASessionScopeInheritsThisBridgesOwnProcessNonce(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	started := bridgeStarted()
	if started == "" {
		t.Fatal("setup: no start time for this process, so the process scope cannot be named")
	}
	own := "bridge-process:" + strconv.Itoa(os.Getpid()) + ":" + started

	before := map[string]any{"name": "architect", "cwd": "/work/dibs"}
	if err := enrichNonce(before, "", own); err != nil {
		t.Fatal("setup: process-scoped enrichment", err)
	}
	handed, _ := before["nonce"].(string)
	if handed == "" {
		t.Fatal("setup: the process scope supplied no nonce")
	}

	after := map[string]any{"name": "architect", "cwd": "/work/dibs"}
	if err := enrichNonce(after, "", "harness-session:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"); err != nil {
		t.Fatal(err)
	}
	if got, _ := after["nonce"].(string); got != handed {
		t.Fatalf("the session scope supplied %q, not the nonce this bridge was handed %q: "+
			"the register is refused while active and forks a sibling while dormant",
			short8(got), short8(handed))
	}

	// Another process's slot is somebody else's evidence and is never borrowed.
	foreign := map[string]any{"name": "auditor", "cwd": "/work/dibs"}
	if err := enrichNonce(foreign, "", "bridge-process:1:not-this-process"); err != nil {
		t.Fatal("setup:", err)
	}
	theirs, _ := foreign["nonce"].(string)
	mine := map[string]any{"name": "auditor", "cwd": "/work/dibs"}
	if err := enrichNonce(mine, "", "harness-session:ffffffff-0000-1111-2222-333333333333"); err != nil {
		t.Fatal(err)
	}
	if got, _ := mine["nonce"].(string); got == theirs {
		t.Error("a session scope adopted another process's credential")
	}
}
