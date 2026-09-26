package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
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
	timer    *time.Timer
	retry    bool // the armed timer is a retry of a delivery that FAILED
	// surrendered says this bridge has given the session socket back to the
	// daemon, because it claimed the route and then could not deliver on it.
	//
	// THE CLAIM IS EVIDENCE OF CAPABILITY, NOT OF DELIVERY, and that gap is a
	// way for the one-writer rule to lose mail entirely. The daemon stands
	// down for an agent whose bridge declared com.dibs/self_wake, so a bridge
	// whose socket has stopped accepting leaves NOBODY writing: the daemon is
	// quiet by arrangement and the bridge is failing into the dark. That is
	// this repository's oldest failure shape, reports success while doing
	// nothing, reintroduced by the change that removed a duplicate.
	//
	// THE ERRNO DECIDES, NOT THE COUNT, and the first cut of this had it
	// wrong. It surrendered after two failures fifteen seconds apart, on the
	// reasoning that surrendering is not free: the daemon's route is the one a
	// session in bypassPermissions holds, so one transient error must not cost
	// it. The caution was right and the discriminator was not, as
	// dibs-coordinator pointed out with this repository's own precedent.
	//
	// A dead socket and a flaky socket differ in the ERROR, not in the
	// frequency. ENOENT or ECONNREFUSED on a session socket is unambiguous and
	// is the COMMON case, because it means the session ended; a timeout is
	// ambiguous and is the rare one. Counting treats them alike, so it paid a
	// fifteen second delay on every ordinary failure to guard against an
	// unusual one, and still surrendered on a genuinely flaky socket the
	// moment the blip happened twice.
	//
	// So: an error that says the socket is GONE surrenders at once, and
	// anything else keeps the retry and surrenders only if that fails too.
	// Same shape as dialFailed one file over, which matches ECONNREFUSED and
	// nothing else on purpose, and the same rule as the AGENTS.md entry about
	// a test that passed thirty times locally: read the error the failing
	// machine reported, because the error names the syscall and the syscall
	// names the branch.
	//
	// Not reclaimed afterwards. Once the daemon is writing again the bridge
	// has nothing to retry with, and the fallback is correct rather than
	// degraded: the daemon sends the same digest this route would have. A
	// session whose socket recovers gets its claim back when its harness next
	// spawns a bridge.
	surrendered bool
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
// arm holds one notice back to the end of the cooldown. Caller holds w.mu and
// has checked that nothing is armed already.
//
// Split out because wake carries three separate concerns already, and the
// deferral is the one with a lifetime of its own: it outlives the call that
// armed it, which is exactly how it came to outlive the subscription too.
func (w *selfWaker) arm(wait time.Duration, notice string) {
	w.pending = true
	recordWakePending(true, notice) // for the in-place upgrade's handoff
	w.timer = time.AfterFunc(wait, func() {
		w.mu.Lock()
		w.pending = false
		w.mu.Unlock()
		recordWakePending(false, "")
		if err := w.wake(notice); err != nil {
			slog.Debug("could not put the deferred notice into this session", "err", err)
		}
	})
}

// wake puts one notice into this session's own queue.
//
// A DEFERRED NOTICE IS ALWAYS DELIVERED, and it was not for four rounds.
//
// A notice held back by the cooldown used to be re-authorised locally before
// delivery: the bridge asked whether the subscription that armed it still
// spoke for this session, and dropped it otherwise. That guard was added
// because a notice could interrupt a session an agent had left during the
// cooldown. It is gone, deliberately, and this is the reasoning, because the
// next reader will be told about that case again.
//
// It asked a question the bridge cannot answer. Whether a subscription is
// still valid depends on token rotation, a session move, a sign-off, a
// recovery through another bridge, and a refusal at the daemon; the check
// stood on a pointer in a local map, which captures none of them. Four
// consecutive review rounds each found another way validity changes without
// that pointer moving, and two of the repairs in between DROPPED notices that
// were owed: both calls return success, both cursors advance, and the mail is
// then announced by nothing at all.
//
// The asymmetry decides it. Delivering a notice late costs one fixed sentence,
// rate limited, into a session that recently held the agent. Dropping one
// costs the message. The first is the failure this product tolerates; the
// second is the failure it exists to prevent.
//
// And the authorisation was already made, by the party that can make it. The
// daemon decides who is woken when it sends the notification, and rounds
// fifty-nine through sixty-eight are that decision. A deferred notice is an
// authorised notice delivered a few seconds later, not a fresh claim to
// re-check. Found by the pre-release review, rounds seventy-two, seventy-three,
// seventy-five and seventy-six, which is three more than this should have
// taken.
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
			w.arm(wait, notice)
		}
		w.mu.Unlock()
		return nil
	}
	prev := w.last
	w.last = now // claimed, and given back below if nothing was delivered
	w.mu.Unlock()

	if err := w.deliver(notice); err != nil {
		// GONE MEANS GONE. No count, no wait: this session's socket is not
		// there, the daemon has stood down on this bridge's word, and until
		// the route goes back nothing announces this agent's mail at all.
		if socketGone(err) {
			w.surrender()
			slog.Warn("giving the session socket back to the daemon: it is not there "+
				"any more and this bridge told the daemon it would deliver this "+
				"agent's wake notices, so nothing would announce them", "err", err)
		}
		// AND TRY AGAIN. The socket was absent or busy; the notice is owed
		// and nothing else will send it until the next arrival. One retry is
		// armed at the cooldown, and folds into any deferred notice already
		// armed. Found by the pre-release review, round eighteen.
		w.mu.Lock()
		if !w.pending {
			w.pending = true
			// THE HANDOFF FOLLOWS THE TIMER. The deferred callback clears the
			// pending mark before it tries, and a delivery that failed there
			// armed this retry without setting it again: an in-place upgrade
			// in that window carried "nothing owed" and the notice was gone
			// with the timer. Found by the pre-release review, round
			// twenty-five.
			recordWakePending(true, notice)
			w.retry = true
			w.timer = time.AfterFunc(w.cooldown, func() {
				w.mu.Lock()
				w.pending, w.retry = false, false
				w.mu.Unlock()
				recordWakePending(false, "")
				if rerr := w.wake(notice); rerr != nil {
					// AMBIGUOUS TWICE IS EVIDENCE. An error naming the socket
					// as gone surrendered on the first attempt; reaching here
					// means the failures were the kind that might have been
					// momentary, and two of those fifteen seconds apart are
					// not momentary any more.
					w.surrender()
					slog.Warn("giving the session socket back to the daemon: two "+
						"attempts fifteen seconds apart failed, so the daemon must "+
						"resume writing or nothing announces this agent's mail",
						"err", rerr)
				}
			})
		}
		w.mu.Unlock()
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
	w.delivered()
	return nil
}

// delivered settles what a successful delivery covers.
//
// A RETRY, AND ONLY A RETRY. A retry armed by a failure stayed armed past a
// delivery that succeeded in the meantime, and put a second notice into the
// session with no mail behind it; this delivery IS that notice, so the retry
// is disarmed. Found by the pre-release review, round thirty-nine. The first
// cut disarmed a DEFERRED notice too, and that one is different: it stands
// for an arrival that came in while this delivery was on the wire, which
// the session has not been told about, and cancelling it lost that mail's
// notice. Found by the pre-release review, round forty-three.
func (w *selfWaker) delivered() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.pending || !w.retry {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.pending, w.retry = false, false
	recordWakePending(false, "")
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

// surrender hands the session socket back to the daemon. See selfWaker.surrendered.
func (w *selfWaker) surrender() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.surrendered = true
}

// canReach reports whether this bridge still claims it can wake its own
// session. Read by listenBody, which declares the claim, and by the watcher,
// which drops its stream when the answer changes so the next listen states the
// truth.
func (w *selfWaker) canReach() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.surrendered
}

// socketGone reports whether this error says the session's socket is not there
// any more, as against being momentarily unusable.
//
// Deliberately a short list, like dialFailed in mcpstdio.go. Everything here
// means the other end is absent: the socket file is unlinked (ENOENT), nothing
// is listening on it (ECONNREFUSED), or the peer closed under the write (EPIPE,
// ECONNRESET). A timeout is NOT here, and that is the point: it does not
// distinguish a dead session from a busy one, so it keeps the retry.
//
// Being wrong in the permissive direction is cheap here and expensive the other
// way. An unnecessary surrender hands the route to the daemon, which sends the
// same digest; a missed one leaves nobody writing at all.
func socketGone(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}
