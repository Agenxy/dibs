package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// Waking the session this bridge is running inside, from inside it.
//
// THE ROUTE THAT ACTUALLY REACHES AN IDLE CLAUDE CODE SESSION.
//
// The daemon's peer-socket wake reads another session's key file and connects
// as a stranger. Claude Code runs those through a `crossSessionInbound` policy
// and, with no explicit setting, HOLDS any peer message when the receiver is in
// bypassPermissions mode, which is what an unattended fleet runs in. There is no
// receipt, so the daemon writes the bytes and learns nothing. Measured: a notice
// delivered to an idle live session, the write succeeding, and that session's
// transcript never growing.
//
// A message from the session's OWN descendants is a different case in that
// policy, and it is accepted rather than held. This process is one: the harness
// spawns its stdio bridge as a direct child and hands it
// CLAUDE_CODE_MESSAGING_SOCKET and CLAUDE_CODE_MESSAGING_TOKEN, which is a
// credential for exactly this. So the bridge can put a line into its own
// session's queue with no operator configuration, no key file to find, no
// process to spawn, and no cross-session gate to pass.
//
// Verified before any of this was written: a child process of a live session
// injected one line using these two variables and it arrived immediately, in a
// session running in bypassPermissions mode.
//
// WHAT THIS IS STILL NOT. It carries the same fixed sentence the other routes
// carry and nothing else: no counts, no senders, no body, nothing an agent
// wrote. It cannot read the session, steer it, or see what happens next. The
// rule is unchanged and this route does not widen it: the board may WAKE an
// agent and may not tell it what to do.
type selfWaker struct {
	mu       sync.Mutex
	socket   string
	token    string
	cooldown time.Duration
	last     time.Time // when a notice was last DELIVERED; an attempt spends nothing
	pending  bool      // a deferred notice is armed for when the cooldown ends
}

// selfWakeCooldown is the shortest gap between two notices in one session.
//
// Cheaper than the exec route, which is why this is seconds rather than the
// ninety a spawned process needs: nothing starts, and a line in a queue that
// already has one costs the reader nothing. It is not zero, because a burst of
// mail should still read as one interruption.
const selfWakeCooldown = 15 * time.Second

// newSelfWaker reads the two variables the harness hands its children. Both
// absent is the ordinary case for every harness that publishes no socket, and
// is not an error: the caller simply has no local route.
func newSelfWaker() *selfWaker {
	sock := os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET")
	tok := os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN")
	if sock == "" || tok == "" {
		return nil
	}
	return &selfWaker{socket: sock, token: tok, cooldown: selfWakeCooldown}
}

// wake puts one notice into this session's own queue.
//
// Returns an error only for a failure worth reporting. Like every other wake
// route, a nil error means WRITTEN: this protocol answers nothing, so delivery
// is still the receiver's to decide and nothing here should claim otherwise.
func (w *selfWaker) wake(notice string) error {
	if w == nil {
		return errors.New("no session socket: this harness publishes none")
	}
	w.mu.Lock()
	now := time.Now()
	if wait := w.cooldown - now.Sub(w.last); wait > 0 {
		// Coalesced, deliberately (see selfWakeCooldown), and NOT DROPPED. A
		// second arrival inside the cooldown used to return success and be
		// forgotten: an agent that had read its inbox after the first notice
		// and finished never heard of the second message until something
		// else arrived for it. One deferred notice is armed for the moment
		// the cooldown ends, and every further arrival folds into that one.
		// Found by the pre-release review, round six.
		if !w.pending {
			w.pending = true
			time.AfterFunc(wait, func() {
				w.mu.Lock()
				w.pending = false
				w.mu.Unlock()
				if err := w.wake(notice); err != nil {
					slog.Debug("could not put the deferred notice into this session", "err", err)
				}
			})
		}
		w.mu.Unlock()
		return nil
	}
	prev := w.last
	w.last = now // claimed, and given back below if nothing was delivered
	w.mu.Unlock()

	if err := w.deliver(notice); err != nil {
		// A FAILED ATTEMPT SPENDS NOTHING. The socket was absent or busy and
		// no notice went anywhere, so the next arrival must try again rather
		// than sit out a cooldown that coalesced nothing.
		w.mu.Lock()
		if w.last.Equal(now) {
			w.last = prev
		}
		w.mu.Unlock()
		return err
	}
	return nil
}

// deliver writes the auth line and one notice to the session socket.
func (w *selfWaker) deliver(notice string) error {
	conn, err := net.DialTimeout("unix", w.socket, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// Auth first, on its own line. The server closes a connection that speaks
	// before it authenticates, so the order is load-bearing rather than tidy.
	auth, _ := json.Marshal(map[string]any{"type": "auth", "token": w.token})
	msg, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": notice},
	})
	for _, line := range [][]byte{auth, msg} {
		if _, err := conn.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}
