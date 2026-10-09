// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// An actual missing socket surrenders the bridge's route. If the harness then
// republishes that socket, only the daemon may write: no old bridge retry.
func TestGoneSocketNeverRetriesAfterSurrender(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false, "") })
	w := newSelfWaker()
	if w == nil {
		t.Fatal("setup: no own-session writer")
	}
	w.cooldown = 100 * time.Millisecond
	var offers atomic.Int32
	w.offerFn = func(string) (string, func(bool), error) {
		offers.Add(1)
		return "original mail", nil, nil
	}
	err := w.wake("original mail")
	if !socketGone(err) || w.canReach() {
		t.Fatalf("setup: absent socket did not surrender: %v", err)
	}
	// Same pathname, new receiver: the daemon owns this recovered route.
	lines := listenLines(t, sock)
	if got := collect(lines, 1, 400*time.Millisecond); len(got) != 0 {
		t.Fatalf("surrendered bridge wrote a timed retry to the recovered socket: %v", got)
	}
	if offers.Load() != 1 || wakeIsPending() {
		t.Fatalf("surrendered route retained retry work: offers=%d pending=%v", offers.Load(), wakeIsPending())
	}
}
