package engine

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// Waking an agent over the socket its own harness publishes.
//
// The alternative to [wake.exec], and the difference that matters is not the
// mechanism but the ADDRESS. A command has to be told which thread to resume,
// so Dibs has to work out which id the agent answers to, and every wake defect
// this project has had is downstream of getting that wrong. A socket is the
// address: it is either there and listening or it is not, and both answers are
// observable rather than silent.
//
// Same notice, same rate limit, same honesty about failure. Nothing here
// decides WHETHER to wake; wakeFor settles that under one lock for both paths,
// because the cooldown, the still-running flag and the deferral were each paid
// for by a bug and a second path that skipped them would re-buy every one.

// peerCacheTTL is how long a discovered set of sessions is reused.
//
// Discovery reads a directory and runs `ps` per candidate, which is far too
// expensive for the writer loop and is why the socket is looked for in the wake
// goroutine rather than in the gate. Sessions appear and vanish on the scale of
// a person opening a terminal, so seconds of staleness costs at most one
// deferred wake, and the retry path already covers that.
const peerCacheTTL = 5 * time.Second

// peerAlive is the liveness check discovery uses, as a variable so a test can
// stand up a fixture session under an invented pid.
//
// A seam, not a weakening: the real check compares the harness's own UTC start
// stamp and is tested directly in internal/peerwake, including against the
// timezone mistake that made it reject every live session on this machine. What
// the engine tests need is a session that exists on disk without a process
// behind it, which no real pid can provide.
var peerAlive = peerwake.Alive

type peerCache struct {
	mu   sync.Mutex
	at   time.Time
	live map[string]peerwake.Session // by session id, and by every alias
}

// peerSessions returns the sessions this machine can deliver to, refreshing at
// most once per TTL.
func (e *Engine) peerSessions() map[string]peerwake.Session {
	// THE DISCOVERY HAPPENS OUTSIDE THE LOCK, and the first version did not.
	//
	// Holding peers.mu across a directory read and a sequential `ps` per
	// candidate made the lock itself the contention: peerSnapshot takes the same
	// mutex, so the "I/O-free" gate on the writer loop waited behind a refresh
	// for up to 300ms per candidate. Removing the I/O from the gate's call path
	// was necessary and not sufficient, and the test written for it proved only
	// that the gate invokes no probe ITSELF, never overlapping it with a
	// refresh, so it could not see the very contention its comment named.
	// Found by the pre-release review, which reproduced the wait.
	//
	// The lock now covers reading the cache and swapping a result in. Two
	// refreshes racing cost one redundant scan and agree on the outcome, which
	// is cheaper than making every board operation wait for either.
	e.peers.mu.Lock()
	fresh := time.Since(e.peers.at) < peerCacheTTL && e.peers.live != nil
	live := e.peers.live
	e.peers.mu.Unlock()
	if fresh {
		return live
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// WARM AND EMPTY, never left cold. Cold means "not looked yet", and the
		// gate refuses on it; a permanent failure that left this nil would make
		// every daemon without a home directory refuse socket wakes forever
		// without ever recording that it had asked. An attempt that found
		// nothing is an answer.
		return e.storePeers(map[string]peerwake.Session{})
	}
	found, err := peerwake.Discover(home, peerAlive)
	if err != nil {
		slog.Debug("peer wake: could not read the harness session directory", "err", err)
	}
	next := make(map[string]peerwake.Session, len(found))
	for _, s := range found {
		next[s.SessionID] = s
	}
	return e.storePeers(next)
}

// storePeers swaps a freshly discovered set in, holding the lock only for that.
func (e *Engine) storePeers(next map[string]peerwake.Session) map[string]peerwake.Session {
	e.peers.mu.Lock()
	defer e.peers.mu.Unlock()
	e.peers.live, e.peers.at = next, time.Now()
	return next
}

// peerSessionFor finds the listening session for this agent, by any of the
// names it answers to.
//
// Aliases as well as the primary, because which of the two a harness quotes is
// exactly what this project spent a night failing to predict. Matching both
// removes the prediction rather than improving it.
// peerSnapshot returns the cache WITHOUT refreshing it.
//
// The refreshing accessor does filesystem work and runs `ps` once per
// candidate, so it must never be reached from the writer loop. This is what the
// gate uses; peerSessions is for the wake goroutine, which is off the loop.
func (e *Engine) peerSnapshot() map[string]peerwake.Session {
	e.peers.mu.Lock()
	defer e.peers.mu.Unlock()
	return e.peers.live
}

// peerSessionIn is the pure lookup: no I/O, no lock, no refresh.
func peerSessionIn(live map[string]peerwake.Session, ids []string) (peerwake.Session, bool) {
	if len(live) == 0 {
		return peerwake.Session{}, false
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if s, ok := live[id]; ok {
			return s, true
		}
	}
	return peerwake.Session{}, false
}

// peerSessionFor looks this agent up, refreshing the cache if it is stale.
// OFF THE WRITER LOOP ONLY.
func (e *Engine) peerSessionFor(ids []string) (peerwake.Session, bool) {
	return peerSessionIn(e.peerSessions(), ids)
}

// mightReachOverSocket is the question the GATE asks: is there any point
// admitting this wake when no command is configured?
//
// Reads the cache and never refreshes, because a refresh is a directory scan
// and a `ps` per candidate and this runs on the writer loop. The cache is
// primed when the engine starts and kept warm by a ticker, so in a running
// daemon it is populated before any event can be published.
//
// STRICT ON A COLD CACHE, and that is deliberate rather than defensive. An
// optimistic answer admits the wake, spends the cooldown and the running flag,
// fails in the goroutine and arms a retry: on a board with no such harness that
// is a loop that never delivers anything. Refusing costs nothing that a warm
// cache does not immediately restore, and it keeps a daemon that has not
// started its background work behaving exactly as it did before any of this
// existed, which is what the guards written for that behaviour assert.
func (e *Engine) mightReachOverSocket(l *core.Agent) bool {
	if l == nil {
		return false
	}
	// THE SNAPSHOT, NEVER THE REFRESH, and the first version of this got it
	// wrong while its own comment described the right thing. It called the
	// lookup that refreshes an expired cache, so with a five-second TTL and a
	// thirty-second background prime, most wake decisions did a directory scan
	// and a `ps` per candidate INLINE on the writer loop, or waited behind a
	// goroutine holding the same mutex. A slow filesystem or a stuck `ps` would
	// stall every board operation. Found by the pre-release review.
	live := e.peerSnapshot()
	if live == nil {
		// NOT LOOKED YET, and after Run that cannot happen: priming is
		// synchronous, before the loop serves anything, so the first event of a
		// daemon's life already has a real answer. This branch is what an engine
		// built without Run sees, which is every unit test that is not about
		// sockets, and strict is the behaviour those guards assert.
		//
		// It was briefly optimistic instead, to avoid losing that first wake.
		// That traded a startup race for a permanent one: an engine that never
		// primes would admit every wake forever. Priming before serving removes
		// the race without the trade, which is why the `ps` behind it is
		// bounded.
		return false
	}
	_, ok := peerSessionIn(live, sessionsOf(l))
	return ok
}

// primePeerSessions fills the cache once, off the writer loop.
//
// Called when the engine starts, so the gate above has a real answer before the
// first event is published rather than refusing the first wake of a session's
// life. Safe to call more than once: peerSessions is TTL-guarded.
func (e *Engine) primePeerSessions() { _ = e.peerSessions() }

// wakeOverSocket delivers one notice and says honestly whether it landed.
func (e *Engine) wakeOverSocket(plan wakePlan, agent string) bool {
	// NO e.state HERE. This runs in a goroutine and the board is single-writer;
	// the plan carries the ids, copied while the loop held still.
	s, ok := e.peerSessionFor(plan.sessions)
	if !ok {
		// Not a failure of delivery: there is nobody listening under any name
		// this agent answers to. Logged at debug because on a board of Codex
		// agents it is the ordinary case and would otherwise be noise.
		slog.Debug("no peer socket for this agent; nothing was woken",
			"agent", agent, "sessions", len(plan.sessions))
		return false
	}
	// A SESSION WORKING SOMEWHERE ELSE IS THE SHAPE OF A MIS-BOUND ID.
	//
	// The wake goes wherever the binding points, and a binding can be wrong: a
	// swept row frees a live session's id and the next agent in that directory
	// inherits it. Before this route existed that misdelivery was invisible,
	// because the wake simply failed. Now it succeeds, into the wrong session,
	// which is more effective and no more correct.
	//
	// Logged, never enforced. The daemon cannot know which of the two is wrong,
	// and refusing on a heuristic would ground legitimate wakes for agents that
	// genuinely moved. Saying so is what turns an invisible misdelivery into
	// one somebody can find.
	//
	// BEFORE the delivery, not after. The mismatch is a fact about which session
	// we are ABOUT to wake, and it is just as true when the write then fails.
	// Reporting it only on success also made the test for it depend on the
	// write landing, which is a race.
	if plan.cwd != "" && s.CWD != "" && plan.cwd != s.CWD {
		slog.Warn("waking a session that reports a different working directory: "+
			"this agent's session id may belong to another session",
			"agent", agent, "session_id", s.SessionID,
			"agent_cwd", plan.cwd, "session_cwd", s.CWD)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := peerwake.Deliver(ctx, s, plan.notice); err != nil {
		// Loud, because this one IS a failure: the session was listening a
		// moment ago and the notice did not get there. Silence here is the
		// class of bug this whole path exists to remove.
		slog.Warn("peer wake failed to deliver",
			"agent", agent, "session_id", s.SessionID, "pid", s.PID, "err", err)
		return false
	}
	// HANDED OVER, not "woken", because that is all this knows.
	//
	// The write succeeding proves the kernel took the bytes. There is no
	// acknowledgement to read, and a correct token is indistinguishable from a
	// wrong one from this side, so claiming the agent was woken asserts three
	// things nobody checked: that the harness authenticated it, that its
	// permission mode allowed it, and that anybody saw it. Saying so in a log is
	// how a wake path comes to report success while doing nothing, which is the
	// failure this whole route was built to remove.
	//
	// The attempt is still spent. Retrying an unacknowledgeable delivery would
	// mean waking on a loop for every message the recipient's own permission
	// mode holds, which is worse than a notice that may not land.
	slog.Info("handed a wake notice to the harness socket; delivery is the "+
		"recipient's to accept",
		"agent", agent, "session_id", s.SessionID, "pid", s.PID)
	return true
}

// harnessSpeaksSocket reports whether this agent's harness is one that
// publishes a session socket, for the send-time warning.
func harnessSpeaksSocket(l *core.Agent) bool {
	if l == nil || l.Agent == nil {
		return false
	}
	return strings.Contains(strings.ToLower(l.Agent.Harness), "claude")
}
