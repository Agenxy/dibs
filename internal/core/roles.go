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
// It gets no power to *read* another lane's mail. Breadth, not intrusion.

// IsCoordinator reports whether the lane may use coordinator powers. Admin
// implies coordinator: a role that could not do less than the tier below it
// would be a trap.
func (l *Lane) IsCoordinator() bool { return l.Role == RoleCoordinator || l.Role == RoleAdmin }

// IsAdmin reports whether the lane holds the full god view: including reading
// other lanes' mail. Only a human grants this.
func (l *Lane) IsAdmin() bool { return l.Role == RoleAdmin }

// applyGrantRole sets a lane's role. The engine admits this op only on the
// admin path (local secret + admin password), so a lane can never promote
// itself or another; the core just applies the recorded decision.
func (s *State) applyGrantRole(op *Op, now time.Time) (Result, []Event, error) {
	l, ok := s.Lanes[op.To]
	if !ok {
		return nil, nil, errf("E_NO_LANE", "check the board for live lanes", "no lane %q", op.To)
	}
	role := op.Mode
	if role != RoleMember && role != RoleCoordinator && role != RoleAdmin {
		return nil, nil, errf("E_BAD_ROLE",
			"use member (default) | coordinator (broadcast + force_release) | admin (everything, including reading all mail)",
			"unknown role %q", role)
	}
	if l.Role == role || (l.Role == "" && role == RoleMember) {
		return Result{"ok": true, "lane": l.ID, "role": role, "changed": false}, nil, nil
	}
	l.Role = role
	evs := []Event{{Type: "lane.role_changed", Lane: l.ID, Data: map[string]any{"role": role}}}
	s.finish(&evs, now)
	return Result{"ok": true, "lane": l.ID, "role": role, "changed": true}, evs, nil
}

// applyForceRelease drops another lane's claim. Coordinator-only, ledgered, and
// reported to the holder: unsticking a shared resource is legitimate, doing it
// invisibly is not.
func (s *State) applyForceRelease(l *Lane, op *Op) (Result, []Event, error) {
	if !l.IsCoordinator() {
		return nil, nil, ErrNotCoordinator
	}
	path := cleanPath(op.Path)
	for i, c := range s.Claims {
		if c.Path != path {
			continue
		}
		holder := c.Lane
		s.Claims = append(s.Claims[:i], s.Claims[i+1:]...)
		return Result{"ok": true, "path": path, "was_held_by": holder},
			[]Event{{
				Type: "claim.force_released", Lane: l.ID, To: holder,
				Data: map[string]any{"path": path, "by": l.ID, "note": op.Note},
			}}, nil
	}
	return nil, nil, errf("E_NO_CLAIM", "list claims via the board", "no claim on %q", path)
}

// LiveLanesExcept returns every live lane other than one, sorted: the engine
// uses it to fan a broadcast out deterministically.
func (s *State) LiveLanesExcept(id string) []*Lane {
	out := make([]*Lane, 0, len(s.Lanes))
	for _, l := range s.Lanes {
		if l.ID == id || l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		out = append(out, l)
	}
	sortLanesByID(out)
	return out
}

func sortLanesByID(ls []*Lane) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && ls[j-1].ID > ls[j].ID; j-- {
			ls[j-1], ls[j] = ls[j], ls[j-1]
		}
	}
}

// LaneBySession finds the lane bound to a harness session id. Used by lifecycle
// hooks, which know their session but hold no lane token.
func (s *State) LaneBySession(sid string) *Lane {
	if sid == "" {
		return nil
	}
	for _, l := range s.Lanes {
		if l.SessionID == sid && l.Status != StatusArchived && l.Status != StatusClosed {
			return l
		}
	}
	return nil
}

// LaneForHook resolves the lane a lifecycle hook is speaking for.
//
// A hook knows what its OWN harness calls the session. That is not always what
// the lane registered with: the stdio bridge supplies `bridge-<pid>-<random>`
// when the model leaves session_id blank, which it always does, so for
// opencode, whose plugin knows only opencode's session id, the two identifiers
// can never match. That mismatch silently disabled both the wake path and the
// claim guard, because a hook that cannot name a lane simply gets nothing back.
//
// cwd is the one identifier both sides observe: the bridge records it from
// os.Getwd(), and a plugin knows the project it is running in. So it is the
// fallback, and deliberately a STRICT one: used only when exactly one live
// lane sits in that directory. Two agents in one checkout is precisely the case
// where guessing would attribute an edit to the wrong lane, and a wrong
// attribution here means allowing a write that should have been refused.
func (s *State) LaneForHook(sid, cwd string) *Lane {
	if l := s.LaneBySession(sid); l != nil {
		return l
	}
	// A session id that was SUPPLIED and matched nothing is positive evidence
	// this is a different session, not a hint to go looking for a neighbour.
	//
	// Without this, the directory fallback below attributed any unregistered
	// session to whichever single registered agent shared its working
	// directory, which is the normal state of two agents in one repository.
	// Verified against a running daemon: a session id that matched no lane was
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
	var found *Lane
	for _, l := range s.Lanes {
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
// exclusive lane's queue, and is exempt from its parent's exclusive claims in
// the guard. Verified against a running daemon before this existed: an agent
// registering with parent:"victim" posted into the victim's exclusive lane,
// joined instead of queueing, and got allow/no-claim for a path the victim held
// exclusively.
//
// Only the parent can issue this, because only the parent holds the token this
// op requires. A genuine subagent is spawned BY its parent, so handing it a
// secret costs nothing; an impostor has no way to obtain one.
func (s *State) applyVouchChild(l *Lane, op *Op) (Result, []Event, error) {
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
	}, []Event{{Type: "lane.vouched_child", Lane: l.ID}}, nil
}
