package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// Round seventy-two's guard reached the deferred path and not the two beside
// it. Both of these were found by the review; both are ways the guard itself
// made things worse than the spurious notice it was added to stop.
func TestTheOwnershipTestReachesEveryDeferredPath(t *testing.T) {
	// A notice from a LIVE stream folding into a timer armed by a retired one
	// must still be delivered. The watcher advances the new stream's cursor on
	// the success this returns, so dropping it loses the wake outright: the
	// reconnect excludes the event and nothing announces the mail.
	t.Run("a live arrival folding into a retired timer is still delivered", func(t *testing.T) {
		sock := sockPath(t)
		t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
		t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
		t.Cleanup(func() { recordWakePending(false) })
		lines := listenLines(t, sock)

		w := &selfWaker{socket: sock, token: "child-token", cooldown: 400 * time.Millisecond}
		var retired atomic.Bool // the old stream's test: never true
		var current atomic.Bool
		current.Store(true) // the replacement's: it is the one holding the key

		// THREE arrivals, because it takes three to reach this. The first
		// DELIVERS and spends the cooldown, so nothing is armed by it; the
		// second arms the timer, and is the one whose stream is later retired;
		// the third folds into that armed timer from a live stream. A test
		// with only two never arms a timer it does not also own, which is how
		// the first cut of this passed against the defect.
		if err := w.wakeWhile(selfWakeNotice, "a", retired.Load); err != nil {
			t.Fatal("setup:", err)
		}
		if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
			t.Fatalf("setup: %d line(s) from the first notice, want 2", len(got))
		}
		if err := w.wakeWhile(selfWakeNotice, "a", retired.Load); err != nil {
			t.Fatal("setup:", err)
		}
		if !currentWakePending() {
			t.Fatal("setup: the second arrival armed no deferred notice, so this proves nothing")
		}
		// And now one from the stream that is current.
		if err := w.wakeWhile(selfWakeNotice, "b", current.Load); err != nil {
			t.Fatal(err)
		}
		if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
			t.Errorf("the live stream's notice folded into a timer owned by a retired one and "+
				"was then dropped: %d line(s) arrived. Its cursor has already advanced, so "+
				"nothing announces this mail at all", len(got))
		}
	})

	// And a retry armed by a delivery that FAILED must not interrupt a session
	// whose subscription has since been retired.
	t.Run("a retry after retirement is dropped", func(t *testing.T) {
		sock := sockPath(t) // a path with nothing listening on it yet
		t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
		t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
		t.Cleanup(func() { recordWakePending(false) })

		w := &selfWaker{socket: sock, token: "child-token", cooldown: 300 * time.Millisecond}
		var live atomic.Bool
		live.Store(true)
		if err := w.wakeWhile(selfWakeNotice, "a", live.Load); err == nil {
			t.Fatal("setup: delivering to a socket nobody is listening on reported success")
		}
		if !currentWakePending() {
			t.Fatal("setup: the failed delivery armed no retry, so this proves nothing")
		}

		// The subscription is retired, and only then does the socket start
		// accepting: the waker itself is never touched, so the retry's own
		// test is the only thing that can stop it.
		live.Store(false)
		lines := listenLines(t, sock)
		if got := collect(lines, 2, 2*time.Second); len(got) != 0 {
			t.Errorf("the retry interrupted a session whose subscription had been retired: "+
				"%d line(s) arrived", len(got))
		}
	})
}

// One waker serves every mailbox on this bridge, so a deferred notice can be
// owed to more than one. Keeping only the newest arrival's test let mailbox
// B's arrival overwrite mailbox A's, and B retiring first then dropped a
// notice A was still owed, with both cursors already advanced past it.
func TestADeferredNoticeSurvivesWhileAnyOwedMailboxIsLive(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false) })
	lines := listenLines(t, sock)

	w := &selfWaker{socket: sock, token: "child-token", cooldown: 400 * time.Millisecond}
	var liveA, liveB atomic.Bool
	liveA.Store(true)
	liveB.Store(true)

	// One delivers and spends the cooldown.
	if err := w.wakeWhile(selfWakeNotice, "a", liveA.Load); err != nil {
		t.Fatal("setup:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: %d line(s) from the first notice, want 2", len(got))
	}
	// Mailbox A arms the deferred notice; mailbox B folds into it.
	if err := w.wakeWhile(selfWakeNotice, "a", liveA.Load); err != nil {
		t.Fatal("setup:", err)
	}
	if err := w.wakeWhile(selfWakeNotice, "b", liveB.Load); err != nil {
		t.Fatal("setup:", err)
	}
	// B retires before the timer fires. A is still current and still owed.
	liveB.Store(false)

	if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
		t.Errorf("the deferred notice was dropped because one contributing mailbox retired, "+
			"while another was still current and still owed it: %d line(s) arrived. Both "+
			"cursors advanced on the success those calls returned, so nothing announces "+
			"this mail now", len(got))
	}
}
