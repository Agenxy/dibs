// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

type socketBindingEvidence struct {
	target string // actual sidecar session, including a lineage-resolved alias
	kind   string // stated, guessed, unknown, conflicting, other-host
}

type socketBindingOwner struct {
	id       string
	conflict bool
}

// Writer-only, I/O-free. Count distinct local stated holders, including old
// ledger bindings today's ingress would refuse. A guessed holder cannot veto
// a stated one; a second stated holder makes the destination ambiguous.
func (e *Engine) socketBindingOwners(live map[string]peerwake.Session) map[string]socketBindingOwner {
	owners := map[string]socketBindingOwner{}
	if e.state == nil {
		return owners
	}
	for _, a := range e.state.Agents {
		if a.Gone() || e.remoteHostOf(a) != "" {
			continue
		}
		for _, sid := range append([]string{a.SessionID}, a.SessionAliases...) {
			if sid == "" || a.GuessedSession(sid) {
				continue
			}
			if peer, ok := live[sid]; ok {
				sid = peer.SessionID
			}
			prior := owners[sid]
			owners[sid] = socketBindingOwner{id: a.ID, conflict: prior.conflict || prior.id != "" && prior.id != a.ID}
		}
	}
	return owners
}

func (e *Engine) socketBindingOf(
	a *core.Agent, sid, target string, owners map[string]socketBindingOwner,
) socketBindingEvidence {
	b := socketBindingEvidence{target: target, kind: "unknown"}
	if a == nil || a.Gone() || !a.HoldsSession(sid) {
		return b
	}
	switch {
	case e.remoteHostOf(a) != "":
		b.kind = "other-host"
	case owners[target].conflict || owners[target].id != "" && owners[target].id != a.ID:
		b.kind = "conflicting"
	case a.GuessedSession(sid):
		b.kind = "guessed"
	case a.Agent != nil && a.Agent.HostID != "":
		b.kind = "stated"
	}
	return b
}

// Only copied values cross into the wake goroutine. Discovery can change
// between planning and execution; evidence for a different target is unknown.
func (e *Engine) socketBindingsFor(a *core.Agent) map[string]socketBindingEvidence {
	live := e.peerSnapshot()
	owners := e.socketBindingOwners(live)
	out := map[string]socketBindingEvidence{}
	for _, sid := range sessionsOf(a) {
		if peer, ok := live[sid]; ok {
			out[sid] = e.socketBindingOf(a, sid, peer.SessionID, owners)
		}
	}
	return out
}

// Refusing beats logging and delivering: a wrong guess interrupts somebody
// else's turn and spends the intended recipient's attempt; a refusal can take
// the existing retry path. That asymmetry still decides guessed/unknown cases.
// A stated same-host binding outranks launch cwd because work can move while
// the harness session stays the same; it is stronger existing evidence.
func socketBindingRefusal(b socketBindingEvidence, differentDirectory bool) string {
	switch b.kind {
	case "conflicting":
		return "conflicting stated session bindings"
	case "other-host":
		return "the agent belongs to another host"
	}
	if differentDirectory && b.kind != "stated" {
		return "different working directory without a stated same-host binding (" + b.kind + ")"
	}
	return ""
}

const socketBindingHint = "From the real harness session, register with the saved nonce or call bind_session " +
	"for this agent; if the working directory changed, call update with the real cwd. " +
	"Do not copy another session's id."

// Both values have been canonicalised at their filesystem ingress. This helper
// is also used on the writer, so it must never resolve paths itself.
func socketDirectoriesDiffer(agent, session string) bool {
	return agent != "" && session != "" && agent != session
}

func (e *Engine) socketBindingView(
	a *core.Agent, live map[string]peerwake.Session, owners map[string]socketBindingOwner,
) core.Result {
	if a == nil || a.Gone() || !e.socketWakesOn() || !harnessSpeaksSocket(a) {
		return nil
	}
	if own, _ := e.SelfWaking(a.ID); own {
		return nil
	}
	peer, sid, ok := peerSessionMatch(live, sessionsOf(a))
	if !ok {
		return nil
	}
	binding := e.socketBindingOf(a, sid, peer.SessionID, owners)
	reason := socketBindingRefusal(binding, socketDirectoriesDiffer(cwdOf(a), peer.CWD))
	if reason == "" {
		return nil
	}
	return core.Result{"state": "refused", "reason": reason, "hint": socketBindingHint}
}

func (e *Engine) decorateBoardSocketBindings(board core.Result) {
	live := e.peerSnapshot()
	owners := e.socketBindingOwners(live)
	rows, _ := board["agents"].([]map[string]any)
	for _, row := range rows {
		id, _ := row["id"].(string)
		if view := e.socketBindingView(e.state.Agents[id], live, owners); view != nil {
			row["socket_wake"] = view
		}
	}
}
