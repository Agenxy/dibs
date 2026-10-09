// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Who is going to wake themselves, so this daemon does not do it too.
//
// TWO WRITERS, ONE SOCKET, AND THE OPERATOR SAW BOTH. A Claude Code session
// has exactly one message socket and Dibs had two independent things writing
// to it: this daemon, which finds the socket through the harness's
// ~/.claude/sessions sidecar and sends the digest, and the session's own stdio
// bridge, which finds it through CLAUDE_CODE_MESSAGING_SOCKET and sent a fixed
// content-free sentence. Neither knew the other existed. The bridge is
// in-process and skips the sidecar lookup, so it won the race every time: the
// operator's screen showed the useless line first and the real digest second,
// each wrapped in the harness's own peer-message preamble. Three separate
// releases reworded the useless line; none of them noticed there were two.
//
// So a bridge that can reach its own session says so when it subscribes, and
// this daemon stands down for that agent. The direction is not a preference.
// The bridge's route is the RELIABLE one: a self-sent message is accepted where
// a stranger's is held in bypassPermissions mode, and the bridge knows for
// certain whether it has a socket. This daemon's route is best effort with no
// receipt, and it cannot tell a held message from a delivered one. The party
// that can prove it will deliver is the party that should.
//
// Registered for the life of the subscription, exactly like a host bridge
// (AttachHostBridgeWith): the stream is a live connection to this daemon, so
// "is a bridge listening" is a fact this process holds rather than a guess. A
// dropped connection releases the claim and the socket route resumes on the
// next wake.
type selfWakers struct {
	mu sync.Mutex
	// live counts the subscriptions per agent that declared they can reach
	// their own session. A count rather than a flag: a reconnecting bridge
	// overlaps its old stream with its new one, and a flag cleared by the old
	// stream's release would hand the socket route back while a working
	// bridge was still attached.
	live map[string]int
	// sessions is the harness session each agent's self-waking bridge named,
	// for the refusal log. Not used to decide: see SelfWaking.
	sessions map[string]string
	claims   map[string]*selfWakeClaim
	closed   map[string]selfWakeClaim // at most the last closed named route per agent
}

// AttachSelfWaker records that an agent's own bridge will put wake notices
// into the session it is running in, and returns the release for when that
// subscription ends. A blank agent registers nothing.
func (e *Engine) AttachSelfWaker(agentID, session string) func() {
	return e.AttachSelfWakerClaim(agentID, session, "")
}

// AttachSelfWakerClaim adds a named route; legacy bridges retain their
// stream-lifetime claim.
// A release of an old stream never clears a newer bridge route.
func (e *Engine) AttachSelfWakerClaim(agentID, session, claim string) func() {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return func() {}
	}
	sw := &e.selfWakers
	sw.mu.Lock()
	if sw.live == nil {
		sw.live = map[string]int{}
		sw.sessions = map[string]string{}
	}
	sw.live[agentID]++
	if session != "" {
		sw.sessions[agentID] = session
	}
	var named *selfWakeClaim
	key := agentID + "\x00" + claim
	if claim != "" {
		if sw.claims == nil {
			sw.claims = map[string]*selfWakeClaim{}
			sw.closed = map[string]selfWakeClaim{}
		}
		named = sw.claims[key]
		if named == nil {
			named = &selfWakeClaim{session: session, id: claim}
			sw.claims[key] = named
		}
		named.count++
		delete(sw.closed, agentID)
	}
	sw.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { sw.detach(agentID, key, named) }) }
}

// SelfWaking reports whether this agent has a live bridge that will deliver
// its own wake notices, and the session that bridge named.
//
// Keyed on the AGENT, not on the session, and that is deliberate. The claim is
// authenticated by the agent's token, and a bridge only ever writes to the
// session it is itself running in, so a self-waking bridge for this agent is
// by construction the session this agent is reachable in. Requiring the two
// session ids to match would hand the socket route back whenever a harness
// published no session id on its listen, which is the case this exists to
// cover.
func (e *Engine) SelfWaking(agentID string) (bool, string) {
	sw := &e.selfWakers
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.live[agentID] > 0, sw.sessions[agentID]
}

type selfWakeClaim struct {
	session, id string
	count       int
	released    bool
}

func (sw *selfWakers) releaseNamed(agent, session, id string) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	key := agent + "\x00" + id
	named := sw.claims[key]
	if named == nil {
		old, ok := sw.closed[agent]
		if !ok || old.id != id || old.session != session || old.released {
			return false
		}
		old.released = true
		sw.closed[agent] = old
		return true
	}
	if named.session != session {
		return false
	}
	delete(sw.claims, key)
	named.released = true
	sw.closed[agent] = *named
	sw.live[agent] -= named.count
	if sw.live[agent] <= 0 {
		delete(sw.live, agent)
		delete(sw.sessions, agent)
	}
	return true
}

// ReleaseSelfWakerFor surrenders only the caller's current-session claim. This is
// background observation: it neither marks mail delivered nor confirms an offer.
// Probe off the writer, then reconsider the original owed mail at this event.
func (e *Engine) ReleaseSelfWakerFor(ctx context.Context, token, session, claim string) error {
	res, err := e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil {
			return core.Result{"error": core.ErrBadToken}
		}
		if !l.SessionIsCurrent(session) || claim == "" {
			return nil
		}
		if !e.selfWakers.releaseNamed(l.ID, session, claim) {
			return nil
		}
		return core.Result{"agent": l.ID}
	})
	if err != nil {
		return err
	}
	if refused, ok := res["error"].(error); ok {
		return refused
	}
	agent, _ := res["agent"].(string)
	if agent == "" {
		return nil
	}
	e.peers.mu.Lock()
	e.peers.at = time.Time{}
	e.peers.mu.Unlock()
	e.primePeerSessions()
	_, err = e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil || l.ID != agent || !l.SessionIsCurrent(session) {
			return nil
		}
		if own, _ := e.SelfWaking(agent); own {
			return nil
		}
		e.wakers.mu.Lock()
		delete(e.wakers.last, agent)
		e.wakers.mu.Unlock()
		if !e.noteArrivalDuringWake(agent) {
			e.retryWakeDecision(agent)
		}
		return nil
	})
	return err
}

func (sw *selfWakers) detach(agent, key string, named *selfWakeClaim) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if named != nil {
		if sw.claims[key] != named {
			return
		} // explicit surrender cannot clear a newer route
		named.count--
		if named.count == 0 {
			delete(sw.claims, key)
			sw.closed[agent] = *named
		}
	}
	if sw.live[agent] <= 1 {
		delete(sw.live, agent)
		delete(sw.sessions, agent)
		return
	}
	sw.live[agent]--
}

func (e *Engine) pruneClosedSelfWakers() {
	sw := &e.selfWakers
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for agent := range sw.closed {
		if l := e.state.Agents[agent]; l == nil || l.Retired() {
			delete(sw.closed, agent)
		}
	}
}
