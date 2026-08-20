package core

import "time"

// Coordinator capabilities.
//
// The shape a fleet actually takes is one or a few trusted agents directing many
// workers. Before this, the human had to relay between them by hand, which is
// exactly the failure in REQUIREMENTS.md. A coordinator gets two powers, chosen
// because each removes a place a human was previously the transport:
//
//   - broadcast  : address the whole fleet at once (implemented in the engine
//     as N ordinary sends, so each message keeps its own serial and ledger
//     entry; a single op cannot own N message identities without breaking the
//     one-change-one-serial invariant).
//   - force_release: unstick a shared resource whose holder is gone, instead
//     of waiting out a lease or restarting the daemon.
//
// It gets no power to read a LIVE agent's mail. Breadth, not intrusion:
// `all_mail` is admin-only, and directing a fleet does not require reading its
// private correspondence.
//
// One exception, stated here because a flat "no power to read another agent's
// mail" was false and this is the file people quote. `adopt_agent` MOVES a
// dormant agent's mailbox onto a live one, and reading it afterwards is the
// entire point: it exists to rescue mail stranded in a row whose owner cannot
// come back, which is this product's most reported failure. A coordinator may
// perform it, so a coordinator can end up holding a dormant peer's messages.
//
// The limits that make that a rescue rather than a back door: the source must
// be non-active, the human's row is refused outright on both the direct call
// and the approve-a-request path, and every adoption is ledgered under the name
// of whoever did it. It is still a real power over private content, granted on
// STATUS rather than on any proof of continuity, and the honest description of
// the role includes it. A pre-release review flagged the older sentence as an
// authorisation contract that the implementation contradicted, and it was
// right; `internal/mcp/staff.md` had already documented the edge for the agents
// holding the role while this file went on denying it.

// IsCoordinator reports whether the agent may use coordinator powers. Admin
// implies coordinator: a role that could not do less than the tier below it
// would be a trap.
func (l *Agent) IsCoordinator() bool { return l.Role == RoleCoordinator || l.Role == RoleAdmin }

// IsAdmin reports whether the agent holds the full god view: including reading
// other agents' mail. Only a human grants this.
func (l *Agent) IsAdmin() bool { return l.Role == RoleAdmin }

// applyGrantRole sets an agent's role. The engine admits this op only on the
// admin path (local secret + admin password), so an agent can never promote
// itself or another; the core just applies the recorded decision.
func (s *State) applyGrantRole(op *Op, now time.Time) (Result, []Event, error) {
	l, ok := s.Agents[op.To]
	if !ok {
		return nil, nil, errf("E_NO_AGENT", "check the board for live agents", "no agent %q", op.To)
	}
	role := op.Mode
	if role != RoleMember && role != RoleCoordinator && role != RoleAdmin {
		return nil, nil, errf("E_BAD_ROLE",
			"use member (default) | coordinator (broadcast + force_release) | admin (everything, including reading all mail)",
			"unknown role %q", role)
	}
	if l.Role == role || (l.Role == "" && role == RoleMember) {
		return Result{"ok": true, "agent": l.ID, "role": role, "changed": false}, nil, nil
	}
	l.Role = role
	evs := []Event{{Type: "agent.role_changed", Agent: l.ID, Data: map[string]any{"role": role}}}
	s.finish(&evs, now)
	return Result{"ok": true, "agent": l.ID, "role": role, "changed": true}, evs, nil
}

// applyForceRelease drops another agent's claim. Coordinator-only, ledgered, and
// reported to the holder: unsticking a shared resource is legitimate, doing it
// invisibly is not.
func (s *State) applyForceRelease(l *Agent, op *Op) (Result, []Event, error) {
	if !l.IsCoordinator() {
		return nil, nil, ErrNotCoordinator
	}
	path := cleanPath(op.Path)
	for i, c := range s.Claims {
		if c.Path != path {
			continue
		}
		holder := c.Agent
		s.Claims = append(s.Claims[:i], s.Claims[i+1:]...)
		return Result{"ok": true, "path": path, "was_held_by": holder},
			[]Event{{
				Type: "claim.force_released", Agent: l.ID, To: holder,
				Data: map[string]any{"path": path, "by": l.ID, "note": op.Note},
			}}, nil
	}
	return nil, nil, errf("E_NO_CLAIM", "list claims via the board", "no claim on %q", path)
}

// LiveAgentsExcept returns every live agent other than one, sorted: the engine
// uses it to fan a broadcast out deterministically.
func (s *State) LiveAgentsExcept(id string) []*Agent {
	out := make([]*Agent, 0, len(s.Agents))
	for _, l := range s.Agents {
		if l.ID == id || l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		out = append(out, l)
	}
	sortAgentsByID(out)
	return out
}

func sortAgentsByID(ls []*Agent) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && ls[j-1].ID > ls[j].ID; j-- {
			ls[j-1], ls[j] = ls[j], ls[j-1]
		}
	}
}

// AgentBySession finds the agent bound to a harness session id. Used by lifecycle
// hooks, which know their session but hold no agent token.
func (s *State) AgentBySession(sid string) *Agent {
	if sid == "" {
		return nil
	}
	for _, l := range s.Agents {
		if l.Status == StatusArchived || l.Status == StatusClosed {
			continue
		}
		if l.SessionID == sid {
			return l
		}
		// Also the names the harness's OTHER half uses for this same session:
		// the bridge says `host-<ppid>`, a configured hook says whatever the
		// harness calls its session. See Agent.SessionAliases.
		for _, alias := range l.SessionAliases {
			if alias == sid {
				return l
			}
		}
	}
	return nil
}

// AgentForHook resolves the agent a lifecycle hook is speaking for.
//
// A hook knows what its OWN harness calls the session. That is not always what
// the agent registered with: the stdio bridge supplies `bridge-<pid>-<random>`
// when the model leaves session_id blank, which it always does, so for
// opencode, whose plugin knows only opencode's session id, the two identifiers
// can never match. That mismatch silently disabled both the wake path and the
// claim guard, because a hook that cannot name an agent simply gets nothing back.
//
// cwd is the one identifier both sides observe: the bridge records it from
// os.Getwd(), and a plugin knows the project it is running in. So it is the
// fallback, and deliberately a STRICT one: used only when exactly one live
// agent sits in that directory. Two agents in one checkout is precisely the case
// where guessing would attribute an edit to the wrong agent, and a wrong
// attribution here means allowing a write that should have been refused.
func (s *State) AgentForHook(sid, cwd string) *Agent {
	if l := s.AgentBySession(sid); l != nil {
		return l
	}
	// A session id that was SUPPLIED and matched nothing is positive evidence
	// this is a different session, not a hint to go looking for a neighbour.
	//
	// Without this, the directory fallback below attributed any unregistered
	// session to whichever single registered agent shared its working
	// directory, which is the normal state of two agents in one repository.
	// Verified against a running daemon: a session id that matched no agent was
	// handed the other agent's private mail INCLUDING the body ("SECRET: the
	// staging password is hunter2"), and guard_path returned
	// decision=allow basis=no-claim for a path that agent held EXCLUSIVELY,
	// the guard resolving the stranger to the claim holder and then reporting
	// that nothing claimed it.
	//
	// The fallback still serves what it was for: a harness whose hook genuinely
	// does not know its session id sends none, and is matched by directory. A
	// harness that sends one is taken at its word.
	if sid != "" {
		return nil
	}
	if cwd == "" {
		return nil
	}
	want := cleanPath(cwd)
	var found *Agent
	for _, l := range s.Agents {
		if l.Status == StatusArchived || l.Status == StatusClosed {
			continue
		}
		if l.Agent == nil || cleanPath(l.Agent.CWD) != want {
			continue
		}
		if found != nil {
			return nil // ambiguous: refuse to guess
		}
		found = l
	}
	return found
}

// applyVouchChild issues a one-time secret the caller's subagent can present as
// proof of lineage.
//
// Parent arrives on the wire as a bare string, and the powers keyed off it are
// not cosmetic: a subagent speaks under its parent's membership, skips an
// exclusive space's queue, and is exempt from its parent's exclusive claims in
// the guard. Verified against a running daemon before this existed: an agent
// registering with parent:"victim" posted into the victim's exclusive space,
// joined instead of queueing, and got allow/no-claim for a path the victim held
// exclusively.
//
// Only the parent can issue this, because only the parent holds the token this
// op requires. A genuine subagent is spawned BY its parent, so handing it a
// secret costs nothing; an impostor has no way to obtain one.
func (s *State) applyVouchChild(l *Agent, op *Op) (Result, []Event, error) {
	if op.Nonce == "" {
		return nil, nil, errf("E_BAD_NONCE",
			"generate a random id (>=128-bit) and give it to the subagent you are spawning; "+
				"it registers with parent_nonce set to that value",
			"nonce required")
	}
	if len(op.Nonce) > s.Limits.MaxIDBytes {
		return nil, nil, errTooLarge("nonce", s.Limits.MaxIDBytes)
	}
	if l.ChildNonces == nil {
		l.ChildNonces = map[string]bool{}
	}
	if len(l.ChildNonces) >= maxChildNonces && !l.ChildNonces[op.Nonce] {
		return nil, nil, errf("E_LIMIT",
			"each voucher is consumed by the subagent that uses it; you have issued "+
				"many that were never used",
			"%d outstanding child vouchers (max)", len(l.ChildNonces))
	}
	l.ChildNonces[op.Nonce] = true
	return Result{
		"ok": true, "parent": l.ID, "outstanding": len(l.ChildNonces),
		"detail": "give this nonce to the subagent; it registers with parent=" + l.ID +
			" and parent_nonce=<nonce>. It is consumed on first use.",
	}, []Event{{Type: "agent.vouched_child", Agent: l.ID}}, nil
}

// CoordinatorID names the agent an addressed role resolves to, preferring a
// LIVE one.
//
// Map iteration is random, so "the first coordinator found" would be a different
// agent on each call and mail addressed to the role would scatter across
// however many hold it. Sorted by id after the liveness preference, so the
// answer is stable: the same board answers the same way twice, which is what
// makes it safe to record the resolution in the ledger.
//
// Preferring a live holder matters on a board like the one this was written
// against, where the standing coordinator was an agent nobody could log back
// into. Addressing the role has to reach somebody who can answer.
func (s *State) CoordinatorID() string {
	best, bestLive := "", false
	for _, l := range s.Agents {
		if l.Status == StatusClosed || l.Status == StatusArchived || !l.IsCoordinator() {
			continue
		}
		live := l.Status == StatusActive
		switch {
		case best == "":
		case live && !bestLive:
		case live == bestLive && l.ID < best:
		default:
			continue
		}
		best, bestLive = l.ID, live
	}
	return best
}

// HasCoordinator reports whether any live agent already holds the role.
//
// Asked once at startup, to decide whether a launch claim is worth minting: a
// board that has settled the question must not offer to settle it again.
func (s *State) HasCoordinator() bool {
	for _, l := range s.Agents {
		if l.Status != StatusClosed && l.IsCoordinator() {
			return true
		}
	}
	return false
}

// maxSessionAliases bounds what one agent accumulates. A session has two names
// in the worst case seen (bridge and hook); the spare room is for a harness
// that restarts its hooks under a new id without the agent re-registering.
const maxSessionAliases = 8

// bindHarnessSession records another name for this agent's harness session,
// returning the id it stored or "" when nothing changed.
//
// Never an id a caller sent: no tool takes this parameter, so it arrives empty
// from every caller and is filled only by the daemon's announced-session join
// (see engine.announcedSession, which is also where the length bound lives). A
// rule here would be retroactive, because this runs in the fold that replays
// the ledger.
//
// Reachable from check_in and update as well as register because the agents it
// fixes are already registered. An agent registered before the join existed has
// no reason to ever register again; without this it stays unreachable by its
// own harness's hooks forever, while every call it makes reports success.
func (a *Agent) bindHarnessSession(sid string) string {
	if sid == "" || sid == a.SessionID {
		return ""
	}
	if a.SessionID == "" {
		a.SessionID = sid
		return sid
	}
	for _, x := range a.SessionAliases {
		if x == sid {
			return ""
		}
	}
	a.SessionAliases = append(a.SessionAliases, sid)
	if n := len(a.SessionAliases); n > maxSessionAliases {
		a.SessionAliases = a.SessionAliases[n-maxSessionAliases:]
	}
	return sid
}
