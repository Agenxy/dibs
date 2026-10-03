package core

import "time"

// Relocation: running an agent somewhere other than where it last ran.
//
// A wake reaches an agent in the environment it runs in and nowhere else (see
// engine/inapp.go). That rule was drawn by the operator after finding their
// ChatGPT threads running in a headless Codex Dibs had started, and it stands
// for every wake. But moving an agent on purpose is a legitimate thing for a
// person or a fleet lead to want: run this app thread headless overnight, or
// pull a terminal thread into the app. So it exists, and it is a separate
// act with its own permission, never something a wake does:
//
//   - the human may always do it (the admin path);
//   - a coordinator or admin may, by role, because directing a fleet is what
//     those roles are for;
//   - any other agent only when the human has granted it PermRelocate.
//
// Every relocation is ledgered with who did it, the agent, where it was and
// where it went, because an agent appearing in an environment it did not start
// in is exactly what an operator will later want explained.

// PermRelocate is the permission to run an agent in a different environment
// from the one it last ran in.
const PermRelocate = "relocate"

// HumanActor is who a relocation records when a person made it.
const HumanActor = "human"

const (
	// OpGrantPermission is the human's, admitted only on the admin path like
	// grant_role: no agent can grant itself anything.
	OpGrantPermission = "grant_permission"
	// OpRevokePermission takes a grant back, on the same path.
	OpRevokePermission = "revoke_permission"
	// OpRelocate is an agent relocating another; OpRelocateByHuman is a person
	// doing it. Two kinds because one carries a token and one never may: the
	// engine refuses a system op that presents a token, and an actor op that
	// does not.
	OpRelocate = "relocate"
	// OpRelocateByHuman is a person moving an agent, on the admin path.
	OpRelocateByHuman = "relocate_by_human"
)

// Relocation is the last time an agent was moved, by whom and between what.
type Relocation struct {
	By   string    `json:"by"`
	From string    `json:"from,omitempty"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

// HasPermission reports whether the agent holds p by an explicit grant.
func (l *Agent) HasPermission(p string) bool {
	for _, have := range l.Permissions {
		if have == p {
			return true
		}
	}
	return false
}

// MayRelocate reports whether the agent may move another agent to a different
// environment: by role, or by the human's grant.
func (l *Agent) MayRelocate() bool { return l.IsCoordinator() || l.HasPermission(PermRelocate) }

// checkPermissionOp rejects a permission nobody has defined, and a relocation
// that does not say what or where. Admit, never Apply: vocabulary is the kind
// of rule that changes, and a vocabulary rule in the fold is retroactive.
func checkPermissionOp(op *Op, lim Limits) error {
	switch op.Kind {
	case OpGrantPermission, OpRevokePermission:
		if op.Mode != PermRelocate && op.Mode != PermQueueOrderLock {
			return errf("E_BAD_PERMISSION", "the one grantable permission is relocate",
				"unknown permission %q", op.Mode)
		}
	case OpRelocate, OpRelocateByHuman:
		if op.To == "" || op.Mode == "" {
			return errf("E_BAD_REQUEST", "name the agent to move and the environment to move it to",
				"relocate needs an agent and an environment")
		}
		if len(op.Mode) > lim.MaxNameBytes {
			return errTooLarge("environment", lim.MaxNameBytes)
		}
	}
	return nil
}

// applyPermission grants or revokes a permission. The engine admits these only
// on the admin path, so the fold applies a decision a person already made.
func (s *State) applyPermission(op *Op, now time.Time) (Result, []Event, error) {
	if op.Mode == PermQueueOrderLock {
		return s.applyQueuePermission(op, now)
	}
	return s.applyPermissionUnscoped(op, now)
}

func (s *State) applyPermissionUnscoped(op *Op, now time.Time) (Result, []Event, error) {
	l, ok := s.Agents[op.To]
	if !ok {
		return nil, nil, errf("E_NO_AGENT", "check the board for the agent's id", "no agent %q", op.To)
	}
	grant := op.Kind == OpGrantPermission
	if l.HasPermission(op.Mode) == grant {
		// Not a transition: no event, no serial, so not ledgered.
		return Result{"ok": true, "agent": l.ID, "permission": op.Mode, "held": grant, "changed": false}, nil, nil
	}
	if grant {
		l.Permissions = append(l.Permissions, op.Mode)
	} else {
		kept := l.Permissions[:0]
		for _, p := range l.Permissions {
			if p != op.Mode {
				kept = append(kept, p)
			}
		}
		l.Permissions = kept
		if len(l.Permissions) == 0 {
			l.Permissions = nil
		}
	}
	evs := []Event{{
		Type: "agent.permission_changed", Agent: l.ID,
		Data: map[string]any{"permission": op.Mode, "held": grant},
	}}
	s.finish(&evs, now)
	return Result{"ok": true, "agent": l.ID, "permission": op.Mode, "held": grant, "changed": true}, evs, nil
}

// applyRelocate records that an agent was moved. by is nil for the human.
//
// The permission is checked HERE as well as at ingress because it depends on
// state (the caller's role and grants), which Admit cannot see; it is the
// force_release pattern. What it reads is ledgered, so replay reaches the same
// answer.
func (s *State) applyRelocate(by *Agent, op *Op, now time.Time) (Result, []Event, error) {
	if by != nil && !by.MayRelocate() {
		return nil, nil, ErrNotPermittedToRelocate
	}
	l, ok := s.Agents[op.To]
	if !ok {
		return nil, nil, errf("E_NO_AGENT", "check the board for the agent's id", "no agent %q", op.To)
	}
	if l.Status == StatusClosed {
		return nil, nil, errf("E_AGENT_CLOSED", "a closed agent has nothing to resume",
			"agent %s is closed", l.ID)
	}
	who := HumanActor
	if by != nil {
		who = by.ID
	}
	from := ""
	if l.Agent != nil {
		from = l.Agent.Surface
	}
	l.Relocated = &Relocation{By: who, From: from, To: op.Mode, At: now}
	evs := []Event{{
		Type: "agent.relocated", Agent: l.ID,
		Data: map[string]any{"by": who, "from": from, "to": op.Mode},
	}}
	s.finish(&evs, now)
	return Result{"ok": true, "agent": l.ID, "by": who, "from": from, "to": op.Mode}, evs, nil
}

// HumanPathOp reports the kinds admitted only on the human's admin path, with
// no agent token: the grants, and a person's relocation. An agent moving
// another is OpRelocate, an actor op with a token.
func HumanPathOp(kind string) bool {
	return kind == OpGrantPermission || kind == OpRevokePermission || kind == OpRelocateByHuman
}
