// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/paths"
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

type lineageSnapshot struct {
	mu      sync.Mutex
	m       map[string][]string
	loading bool
}

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
	return e.storePeers(discoverPeers(home, e.lineages()))
}

// storePeers swaps a freshly discovered set in, holding the lock only for that.
func discoverPeers(home string, lineages map[string][]string) map[string]peerwake.Session {
	found, err := peerwake.Discover(home, peerAlive)
	if err != nil {
		slog.Debug("peer wake: could not read the harness session directory", "err", err)
	}
	next := make(map[string]peerwake.Session, len(found))
	for i := range found {
		// Filesystem edge only: cached diagnostics on the writer compare
		// values and must not resolve paths or scan the machine.
		found[i].CWD = paths.Canonical(found[i].CWD)
		s := found[i]
		next[s.SessionID] = s
	}
	indexByLineage(next, found, lineages)
	return next
}

// refreshPeersNow rebuilds the cache with the app's lineage read now, for a
// caller off the writer that has just learned the app runs a session under
// an id the cache may not have linked yet.
func (e *Engine) refreshPeersNow() map[string]peerwake.Session {
	home, err := os.UserHomeDir()
	if err != nil {
		return e.storePeers(map[string]peerwake.Session{})
	}
	lineages := claudeLineages()
	e.lin.mu.Lock()
	e.lin.m = lineages
	e.lin.mu.Unlock()
	return e.storePeers(discoverPeers(home, lineages))
}

// lineages returns the last lineage read and starts the next one in the
// background. The first read parses every app record (about 0.2 s on the
// machine measured, far more under -race) and the cache is primed before the
// writer serves, so it must never wait on one.
func (e *Engine) lineages() map[string][]string {
	e.lin.mu.Lock()
	defer e.lin.mu.Unlock()
	if !e.lin.loading {
		e.lin.loading = true
		// The source is chosen HERE, on the caller's goroutine: the refresh
		// outlives this call, and reading the package seam from inside it
		// raced every test that restores the seam when it finishes.
		read := claudeLineages
		go func() {
			next := read()
			e.lin.mu.Lock()
			e.lin.m, e.lin.loading = next, false
			e.lin.mu.Unlock()
		}()
	}
	return e.lin.m
}

// claudeLineages is the Claude app's own record of each session's earlier
// ids. Tests replace it; it reads only the app's files.
var claudeLineages = harnessenv.ClaudeLineages

// indexByLineage also files each live session under the earlier ids its app
// session has run as. The app gives a session a new Claude Code id on every
// resume, and Dibs knows an agent by the id it last saw: agenxy-supply on
// 2026-10-11 was bound to 7d767ad4 while its session ran as 007f1c48 and then
// da98da97, so every wake found no socket under the bound id, reopened an
// app session that was already open, and woke nothing for eight hours.
//
// A live session's own id always wins, and an earlier id two live sessions
// both claim is filed under neither: a guess between them could wake the
// wrong one. The working-directory check in wakeOverSocket still applies.
func indexByLineage(next map[string]peerwake.Session, found []peerwake.Session, lineages map[string][]string) {
	claims := map[string][]peerwake.Session{}
	for _, s := range found {
		for _, prior := range lineages[s.SessionID] {
			if _, direct := next[prior]; !direct {
				claims[prior] = append(claims[prior], s)
			}
		}
	}
	for prior, sessions := range claims {
		if len(sessions) == 1 {
			next[prior] = sessions[0]
		}
	}
}

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
	s, _, ok := peerSessionMatch(live, ids)
	return s, ok
}

func peerSessionMatch(live map[string]peerwake.Session, ids []string) (peerwake.Session, string, bool) {
	if len(live) == 0 {
		return peerwake.Session{}, "", false
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if s, ok := live[id]; ok {
			return s, id, true
		}
	}
	return peerwake.Session{}, "", false
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
	if l == nil || !e.socketWakesOn() {
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
	if !e.socketWakesOn() {
		return false // [wake] sockets = false: the operator asked for no such activation
	}
	// THE AGENT'S OWN BRIDGE IS GOING TO DO THIS, so this daemon does not.
	//
	// One session, one message socket, and Dibs was writing to it from two
	// places that did not know about each other: here, through the harness's
	// session sidecar, and the session's own stdio bridge, through the socket
	// path the harness hands its children. The bridge is in-process and skips
	// the sidecar lookup, so it always arrived first, and the operator saw two
	// cards for one message with the less informative one on top. Reported four
	// times; reworded three times; counted once.
	//
	// Stood down HERE rather than at plan time, which matters. Refusing the
	// socket plan would send wakeFor on to the operator's [wake.exec] command
	// and start a process to announce a message that is already being handed to
	// a live session. The plan stays a socket plan, the write is skipped, and
	// the attempt counts as spent: the notice is on its way by the route that
	// does not have to guess whether the receiver will accept it.
	//
	// Reported as DELIVERED, which is a stronger claim than this route usually
	// makes and is justified here. The write below proves only that the kernel
	// took the bytes; a self-sent message is accepted where a stranger's is
	// held in bypassPermissions mode, so handing it to the bridge is the better
	// evidence, not the weaker one.
	if selfWaking, session := e.SelfWaking(agent); selfWaking {
		slog.Debug("not writing to this session: the agent's own bridge is "+
			"subscribed and delivers its own wake notices, and two writers on "+
			"one socket is two notifications for one message",
			"agent", agent, "session_id", session)
		return true
	}
	s, matched, ok := peerSessionMatch(e.peerSessions(), plan.sessions)
	if !ok {
		// Not a failure of delivery: there is nobody listening under any name
		// this agent answers to. Logged at debug because on a board of Codex
		// agents it is the ordinary case and would otherwise be noise.
		slog.Debug("no peer socket for this agent; nothing was woken",
			"agent", agent, "sessions", len(plan.sessions))
		return false
	}
	// A launch directory is weaker evidence than an explicit same-host
	// session binding. Guesses still need matching directories, and competing
	// stated holders are ambiguous even when their directories agree.
	binding := plan.socketBindings[matched]
	if binding.target != s.SessionID {
		binding = socketBindingEvidence{kind: "unknown"}
	}
	different := socketDirectoriesDiffer(paths.Canonical(plan.cwd), s.CWD)
	if reason := socketBindingRefusal(binding, different); reason != "" {
		slog.Warn("refusing socket wake: "+reason, "agent", agent, "session_id", s.SessionID,
			"agent_cwd", plan.cwd, "session_cwd", s.CWD, "hint", socketBindingHint)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var offer string
	if plan.trackOffer && plan.agent != "" {
		r, err := e.socketOfferForPlan(ctx, plan, matched, s.SessionID, different)
		if err != nil {
			return false
		}
		if reason, _ := r["refused"].(string); reason != "" {
			slog.Warn("refusing socket wake: "+reason, "agent", agent, "session_id", s.SessionID, "hint", socketBindingHint)
			return false
		}
		plan.notice, _ = r["digest"].(string)
		offer, _ = r["offer"].(string)
		if plan.notice == "" {
			return true
		}
	}
	if different {
		slog.Info("stated binding outranks cwd difference", "agent", agent, "session_id", s.SessionID,
			"agent_cwd", plan.cwd, "session_cwd", s.CWD)
	}
	written := false
	if offer != "" {
		defer func() {
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer finishCancel()
			_, _ = e.query(finishCtx, func() core.Result {
				e.settleSocketOffer(agent, plan.session, offer, written)
				return nil
			})
		}()
	}
	if err := peerwake.Deliver(ctx, s, plan.notice); err != nil {
		// Loud, because this one IS a failure: the session was listening a
		// moment ago and the notice did not get there. Silence here is the
		// class of bug this whole path exists to remove.
		slog.Warn("peer wake failed to deliver",
			"agent", agent, "session_id", s.SessionID, "pid", s.PID, "err", err)
		return false
	}
	written = true
	if plan.socketWritten != nil {
		*plan.socketWritten = true
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

func (e *Engine) socketOfferForPlan(
	ctx context.Context, plan wakePlan, matched, target string, different bool,
) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		l := e.state.Agents[plan.agent]
		if l == nil || l.CreatedSerial != plan.createdSerial || cwdOf(l) != plan.cwd || !l.HoldsSession(matched) ||
			!l.SessionIsCurrent(plan.session) || !e.socketDaemonReady(l, time.Now()) {
			return core.Result{"digest": ""}
		}
		// Recheck the current holders at the last writer checkpoint. A plan
		// copied before a conflicting bind must not authorize a later write.
		binding := e.socketBindingOf(l, matched, target, e.socketBindingOwners(e.peerSnapshot()))
		if reason := socketBindingRefusal(binding, different); reason != "" {
			return core.Result{"refused": reason}
		}
		rows := e.socketParticipants(l)
		for _, row := range rows {
			if own, _ := e.SelfWaking(row.ID); own {
				return core.Result{"digest": ""} // one bridge writer for this host/session
			}
		}
		return e.beginSocketOffers(rows, plan.session)
	})
}

// harnessSpeaksSocket reports whether this agent's harness is one that
// publishes a session socket, for the send-time warning.
func harnessSpeaksSocket(l *core.Agent) bool {
	if l == nil || l.Agent == nil {
		return false
	}
	return strings.Contains(strings.ToLower(l.Agent.Harness), "claude")
}
