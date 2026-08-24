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
	e.peers.mu.Lock()
	defer e.peers.mu.Unlock()
	if time.Since(e.peers.at) < peerCacheTTL && e.peers.live != nil {
		return e.peers.live
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	found, err := peerwake.Discover(home, peerAlive)
	if err != nil {
		slog.Debug("peer wake: could not read the harness session directory", "err", err)
	}
	live := make(map[string]peerwake.Session, len(found))
	for _, s := range found {
		live[s.SessionID] = s
	}
	e.peers.live, e.peers.at = live, time.Now()
	return live
}

// peerSessionFor finds the listening session for this agent, by any of the
// names it answers to.
//
// Aliases as well as the primary, because which of the two a harness quotes is
// exactly what this project spent a night failing to predict. Matching both
// removes the prediction rather than improving it.
func (e *Engine) peerSessionFor(l *core.Agent) (peerwake.Session, bool) {
	if l == nil {
		return peerwake.Session{}, false
	}
	live := e.peerSessions()
	if len(live) == 0 {
		return peerwake.Session{}, false
	}
	for _, id := range append([]string{l.SessionID}, l.SessionAliases...) {
		if id == "" {
			continue
		}
		if s, ok := live[id]; ok {
			return s, true
		}
	}
	return peerwake.Session{}, false
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
	e.peers.mu.Lock()
	cold := e.peers.live == nil
	e.peers.mu.Unlock()
	if cold {
		return false
	}
	_, ok := e.peerSessionFor(l)
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
	l := e.state.Agents[plan.agent]
	s, ok := e.peerSessionFor(l)
	if !ok {
		// Not a failure of delivery: there is nobody listening under any name
		// this agent answers to. Logged at debug because on a board of Codex
		// agents it is the ordinary case and would otherwise be noise.
		slog.Debug("no peer socket for this agent; nothing was woken",
			"agent", agent, "session_id", plan.session)
		return false
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
	slog.Info("woke an agent over its harness socket",
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
