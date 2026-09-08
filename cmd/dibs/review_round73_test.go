package main

import (
	"testing"
	"time"
)

// A DEFERRED NOTICE IS ALWAYS DELIVERED.
//
// Rounds seventy-two through seventy-six tried to re-authorise a held-back
// notice locally, dropping it when the subscription that armed it looked
// retired. Every round found another way validity changes that the local check
// could not see, and two of the repairs dropped notices that were owed, which
// loses the message: both calls return success, both cursors advance, and
// nothing announces the mail afterwards.
//
// The guarantee is the simple one now, and this pins it: whatever happens to
// the subscription during the cooldown, the notice lands. The daemon decided
// who to wake when it sent the notification; delivering that decision a few
// seconds later is not a fresh claim to re-check.
func TestADeferredNoticeIsDeliveredWhateverBecomesOfItsSubscription(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false) })
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

	// One notice lands and spends the cooldown.
	if err := w.wake(selfWakeNotice); err != nil {
		t.Fatal("setup:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: %d line(s) from the first notice, want 2", len(got))
	}
	// A second arrives inside the cooldown and is held back.
	if err := w.wake(selfWakeNotice); err != nil {
		t.Fatal(err)
	}
	if !currentWakePending() {
		t.Fatal("setup: nothing was deferred, so this proves nothing")
	}
	// The subscription is retired entirely before it fires.
	iw.mu.Lock()
	iw.streams = map[string]*inboxStream{}
	iw.mu.Unlock()

	if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
		t.Errorf("the deferred notice was dropped (%d line(s) arrived). Its cursor has already "+
			"advanced on the success the fold returned, so the mail it was announcing is now "+
			"announced by nothing at all", len(got))
	}
}

// And a retry armed by a delivery that FAILED lands too, for the same reason.
func TestARetriedNoticeIsDeliveredAfterItsSubscriptionGoes(t *testing.T) {
	sock := sockPath(t) // nothing listening yet, so the first delivery fails
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false) })

	w := &selfWaker{socket: sock, token: "child-token", cooldown: 300 * time.Millisecond}
	if err := w.wake(selfWakeNotice); err == nil {
		t.Fatal("setup: delivering to a socket nobody is listening on reported success")
	}
	if !currentWakePending() {
		t.Fatal("setup: the failed delivery armed no retry, so this proves nothing")
	}
	lines := listenLines(t, sock)
	if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
		t.Errorf("the retry was dropped (%d line(s) arrived): a notice owed to the session was "+
			"lost because its subscription had moved on", len(got))
	}
}
