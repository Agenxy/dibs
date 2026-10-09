// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
// It carries the daemon's authenticated digest, never an instruction invented
// here. It cannot read the session or decide what the agent does next.
type selfWaker struct {
	mu       sync.Mutex
	socket   string
	token    string
	cooldown time.Duration
	last     time.Time // when a notice was last DELIVERED; an attempt spends nothing
	pending  bool      // one failed delivery is awaiting its retry
	timer    *time.Timer
	retry    bool   // the armed timer is a retry of a delivery that FAILED
	version  uint64 // new events fence a previous failure callback
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
	// deliverFn replaces the write, for the one branch no platform reaches the
	// same way twice.
	//
	// THE AMBIGUOUS FAILURE CANNOT BE PRODUCED PORTABLY, and the CI gate is
	// what proved it. The retry branch needs an error that is NOT in
	// socketGone, and the obvious fixture, dialling a path that is not a
	// socket, answers ENOTSOCK on macOS and ECONNREFUSED on Linux. The second
	// is in the gone list, so the same test exercised the immediate surrender
	// on one machine and the retry on the other: the AGENTS.md rule about two
	// machines producing different errors for one event, met head on while
	// writing a test about errors.
	//
	// A seam rather than a per-platform fixture table, because the branch is
	// about what the code does with an error and not about which error a
	// kernel picks. Nil in production, where deliver is the only writer.
	deliverFn func(notice string) error
	// refreshFn asks the daemon for current unread state before EVERY write,
	// including failed-delivery retries and upgrade handoffs. Nil in transport tests.
	refreshFn func(notice string) (string, error)
	// Additive daemon capability: refresh plus an attempt receipt. The
	// receipt reports a write, not acceptance; later turn activity confirms it.
	offerFn      func(notice string) (string, func(bool), error)
	retryOfferFn func(notice string) (string, func(bool), error)
}

var errWakeEmpty = errors.New("the fresh wake digest is empty")

// send performs this waker's write: the session socket, or a test's stand-in.
func (w *selfWaker) send(notice string) error {
	return w.sendUsing(notice, w.offerFn)
}

func (w *selfWaker) sendUsing(notice string, offer func(string) (string, func(bool), error)) error {
	written := false
	if offer != nil {
		fresh, finish, err := offer(notice)
		if finish != nil {
			defer func() { finish(written) }()
		}
		if err != nil {
			return err
		}
		notice = fresh
		if notice == "" {
			return errWakeEmpty
		}
	} else if w.refreshFn != nil {
		var err error
		notice, err = w.refreshFn(notice)
		if err != nil {
			return err
		}
		if notice == "" {
			return errWakeEmpty
		}
	}
	if w.deliverFn != nil {
		err := w.deliverFn(notice)
		written = err == nil
		return err
	}
	err := w.deliver(notice)
	written = err == nil
	return err
}

// selfWakeCooldown is the delay before the one failed-delivery retry.
// Successful delivery never delays a later mail event.
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

// Each mail event writes immediately. A failed write may retry once; a later
// event supersedes that retry, and a success or empty fresh read settles it.
func (w *selfWaker) wake(notice string) error {
	if w == nil {
		return errors.New("no session socket: this harness publishes none")
	}
	w.mu.Lock()
	w.version++
	version := w.version
	if w.timer != nil {
		w.timer.Stop()
	}
	w.pending, w.retry = false, false
	w.mu.Unlock()
	recordWakePending(false, "")
	err := w.send(notice)
	if errors.Is(err, errWakeEmpty) {
		return nil
	}
	if err == nil {
		w.mu.Lock()
		w.last = time.Now()
		w.mu.Unlock()
		return nil
	}
	if socketGone(err) {
		w.surrender()
	}
	w.mu.Lock()
	if w.version == version {
		w.pending, w.retry = true, true
		recordWakePending(true, notice)
		w.timer = time.AfterFunc(w.cooldown, func() { w.retryFailed(notice, version) })
	}
	w.mu.Unlock()
	return err
}

func (w *selfWaker) retryFailed(notice string, version uint64) {
	w.mu.Lock()
	if w.version != version || !w.pending || !w.retry {
		w.mu.Unlock()
		return
	}
	w.pending, w.retry = false, false
	w.mu.Unlock()
	recordWakePending(false, "")
	offer := w.retryOfferFn
	if offer == nil {
		offer = w.offerFn
	}
	err := w.sendUsing(notice, offer) // authenticated original-cause fence
	if errors.Is(err, errWakeEmpty) {
		return
	}
	if err != nil {
		w.surrender()
		slog.Warn("giving the session socket back to the daemon: the failed delivery's one retry failed", "err", err)
		return
	}
	w.mu.Lock()
	w.last = time.Now()
	w.mu.Unlock()
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
// MEASURED ON AF_UNIX, macOS, 2026-09-26, because a list whose whole job is to
// be exhaustive should not be guesswork. A listener that closes UNLINKS its
// socket file, so the next dial is ENOENT; that is the common case and what
// the end of a session actually looks like. A write to a peer that has closed
// is EPIPE. A path that is not a socket is ENOTSOCK, which is NOT here on
// purpose: it means a misconfigured path rather than a session that ended, and
// the retry costs little.
//
// ECONNRESET IS DEFENSIVE AND WAS NOT REPRODUCIBLE HERE. Neither way a peer can
// go away produced it on this transport; it is the TCP-shaped answer. Kept
// because the cost of carrying it is nothing and a future platform may differ,
// and said plainly rather than left looking measured, so the next reader does
// not spend a week trying to hit it.
//
// AND THE SAME ERRNO IS CLASSIFIED THE OTHER WAY, DELIBERATELY, IN dialFailed
// (mcpstdio.go). That one matches ECONNREFUSED and nothing else, because there
// a reset cannot prove the request was not already applied and a retried claim
// is worse than a failed call. The stake is what differs, not the ambiguity:
// nothing is APPLIED on this path, so a wrong answer here costs a duplicate
// card or a notice held in a bypass session, where a wrong answer there costs
// a double write to the board. Same ambiguity, opposite resolution, both
// correct. Noted at both ends because "a rule applied at one call site and not
// its siblings" is this repository's recurring class, and a sweep that finds
// these two disagreeing has even odds of reconciling them the wrong way.
// Raised by dibs-coordinator, who spotted the divergence before it was written.
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
