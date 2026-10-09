// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"syscall"
	"testing"
	"time"
)

// A successful second arrival is written before subscription retirement;
// retirement must not leave a scheduled delivery of already announced mail.
func TestNewMailIsDeliveredBeforeItsSubscriptionGoes(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)

	var iw inboxWatcher
	iw.cooldown = 400 * time.Millisecond
	w := iw.sharedWaker()
	if w == nil {
		t.Fatal("setup: no waker")
	}
	st := &inboxStream{key: "busy", token: "tok", session: "host-1"}
	iw.mu.Lock()
	iw.streams = map[string]*inboxStream{"busy": st}
	iw.mu.Unlock()

	// First notice lands.
	if err := w.wake(testWakeNotice); err != nil {
		t.Fatal("setup:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: %d line(s) from the first notice, want 2", len(got))
	}
	// A second arrival is immediate, even inside the failure-retry delay.
	if err := w.wake(testWakeNotice); err != nil {
		t.Fatal(err)
	}
	if got := collect(lines, 2, 200*time.Millisecond); len(got) != 2 {
		t.Fatalf("second arrival delayed: %d line(s)", len(got))
	}
	if wakeIsPending() {
		t.Fatal("successful mail armed a timer")
	}
	// Retire the subscription after both actual writes.
	iw.mu.Lock()
	iw.streams = map[string]*inboxStream{}
	iw.mu.Unlock()

	if got := collect(lines, 1, 2*iw.cooldown); len(got) != 0 {
		t.Fatalf("subscription retirement left a repeated notice: %d line(s)", len(got))
	}
}

// And a retry armed by an ambiguous FAILED write lands too, for the same reason.
func TestARetriedNoticeIsDeliveredAfterItsSubscriptionGoes(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false, "") })

	w := &selfWaker{socket: sock, token: "child-token", cooldown: 300 * time.Millisecond}
	// Keep the callback fixed while the timer is running; replace only the
	// first ambiguous kernel failure, then use the real socket writer.
	first := true
	w.deliverFn = func(notice string) error {
		if first {
			first = false
			return syscall.ETIMEDOUT
		}
		return w.deliver(notice)
	}
	if err := w.wake(testWakeNotice); err == nil {
		t.Fatal("setup: an ambiguous failed write reported success")
	}
	if !wakeIsPending() {
		t.Fatal("setup: the failed delivery armed no retry, so this proves nothing")
	}
	lines := listenLines(t, sock)
	if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
		t.Errorf("the retry was dropped (%d line(s) arrived): a notice owed to the session was "+
			"lost because its subscription had moved on", len(got))
	}
}
