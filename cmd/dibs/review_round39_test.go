package main

import (
	"testing"
	"time"
)

// A retry armed by a failed delivery stayed armed past a delivery that
// succeeded in the meantime: the socket came back, the next arrival was
// delivered, and the timer put a second notice into the session with no
// further mail behind it. The delivery is the notice the retry would have
// given, so it disarms the retry.
func TestASuccessfulDeliveryDisarmsThePendingRetry(t *testing.T) {
	t.Cleanup(func() { recordWakePending(false) })
	sock := sockPath(t)
	w := &selfWaker{socket: sock, token: "tok", cooldown: 300 * time.Millisecond}
	if err := w.wake(selfWakeNotice); err == nil {
		t.Fatal("setup: a wake with nobody listening reported success")
	}
	if !currentWakePending() {
		t.Fatal("setup: the failed delivery armed no retry")
	}
	// The socket comes back before the retry fires, and mail arrives.
	lines := listenLines(t, sock)
	if err := w.wake(selfWakeNotice); err != nil {
		t.Fatal("setup: the delivery with a listener failed:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: %d line(s) from the delivery, want 2", len(got))
	}
	if currentWakePending() {
		t.Fatal("after a successful delivery the handoff still says a notice is owed")
	}
	// Past the retry's due time: nothing more arrives.
	if got := collect(lines, 1, 2*w.cooldown); len(got) != 0 {
		t.Fatalf("the retry fired after a delivery had succeeded: %d extra line(s), a second notice "+
			"with no mail behind it", len(got))
	}
}
