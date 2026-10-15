// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"testing"
	"time"
)

func TestSelfWakeRechecksSessionAfterRefreshingDigest(t *testing.T) {
	previous := threadServed()
	noteThread("71f97290-0001-4000-8000-111111111111")
	t.Cleanup(func() { noteThread(previous); recordWakePending(false, "") })
	sock := sockPath(t)
	lines := listenLines(t, sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "fixture-child-token")
	fixtureSelfWakeIdentity(t, sock)
	w := newSelfWaker()
	if w == nil {
		t.Fatal("setup: no proven own-session route")
	}
	w.offerFn = func(string) (string, func(bool), error) {
		// A real incoming MCP call can change the served thread while the digest
		// refresh is waiting on HTTP. Enter the actual metadata enrichment path.
		enrichRegister([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_in","arguments":{},"_meta":{"threadId":"71f97290-0001-4000-8000-222222222222"}}}`))
		return "foreign-private-refreshed-digest", nil, nil
	}
	if err := w.wake("old-notice"); err != nil {
		t.Fatal(err)
	}
	if wire := collect(lines, 2, 300*time.Millisecond); len(wire) != 0 {
		t.Fatalf("refresh changed the served session but wrote into the old socket: %v", wire)
	}
}
