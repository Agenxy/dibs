package core

import (
	"slices"
	"time"
)

// applyMerge folds a forked row back into the seat it should have been.
//
// THE FORK THIS REPAIRS. A persistent agent's nonce is the credential that
// survives its process, and an agent cannot be relied on to carry a secret
// across a context boundary: its context ends, which is the event the nonce
// exists for, and the nonce goes with it. The next session registers under the
// same name, becomes a SIBLING, and cannot read a word of its predecessor's
// mail. The bridge now keeps the nonce and that is the fix; this is the repair
// for every board that already has the scars. Measured on one: nine rows for
// five roles.
//
// WHAT MOVES AND WHY THAT LIST. Mail, because a mailbox nobody can open is the
// whole injury. Claims, because a path held by a row nobody is behind is a
// path nothing can release. Space membership, for the same reason. The
// survivor's own identity, name and role are NOT touched: an admin naming a
// survivor has said which row is the seat, and quietly renaming it would make
// the repair a second surprise.
//
// WHAT IT REFUSES. Two LIVE agents are two agents, whatever they are called,
// and merging them would redirect one running process's mail into another's
// mailbox mid-flight. The fork being absorbed must not be active. Validation
// is in Admit, where it belongs: a rule added here is retroactive, replay runs
// Apply over ops accepted by older code, and the daemon would refuse its own
// ledger.
func (s *State) applyMerge(op *Op, now time.Time) (Result, []Event, error) {
	if err := s.checkMerge(op); err != nil {
		return nil, nil, err
	}
	from, into := s.Agents[op.To], s.Agents[op.MergeInto]
	if from == nil || into == nil {
		// Admit has already checked this; here it is the replay case, where an
		// agent named by a historical op has since been swept away. Refusing
		// would stop the fold on a board that once merged successfully.
		return Result{"ok": true, "merged": 0, "serial": s.Serial}, nil, nil
	}

	moved := s.moveMail(from.ID, into.ID)
	claims := s.moveClaims(from.ID, into.ID)
	spaces := s.moveMembership(from.ID, into.ID)
	// THE NONCE FOLLOWS THE MAIL. A nonce still pointing at the absorbed row
	// would let the next session reopen the fork this call just closed, which
	// is the repair undoing itself on the next restart.
	for nonce, id := range s.Nonces {
		if id == from.ID {
			s.Nonces[nonce] = into.ID
		}
	}

	from.Status, from.Token = StatusClosed, ""
	from.MergedInto = into.ID
	evs := []Event{{Type: "agent.merged", Agent: into.ID, Data: map[string]any{
		"from": from.ID, "messages": moved, "claims": claims, "spaces": spaces,
	}}}
	serial := s.finish(&evs, now)
	return Result{
		"ok": true, "into": into.ID, "from": from.ID,
		"messages": moved, "claims": claims, "spaces": spaces, "serial": serial,
	}, evs, nil
}

// moveMail hands the fork's mailbox to the survivor, both directions.
//
// The SENDER is rewritten too, so a thread reads coherently afterwards: a
// reply addressed to the fork would otherwise arrive at a row nobody holds.
// Sorted, because the fold has to be deterministic.
func (s *State) moveMail(from, into string) int {
	moved := 0
	for _, serial := range sortedMessageKeys(s.Messages) {
		m := s.Messages[serial]
		if m.To == from {
			m.To = into
			moved++
		}
		if m.From == from {
			m.From = into
		}
	}
	return moved
}

// moveClaims hands over every path the fork held.
//
// A path BOTH rows hold is not duplicated. checkMerge refuses that outright,
// so reaching here with one means a historical op, and dropping the duplicate
// is the only coherent fold.
func (s *State) moveClaims(from, into string) int {
	n := 0
	for _, c := range s.Claims {
		if c.Agent != from || s.claimOn(c.Path, into) != nil {
			continue
		}
		c.Agent = into
		n++
	}
	s.Claims = slices.DeleteFunc(s.Claims, func(c *Claim) bool { return c.Agent == from })
	return n
}

// moveMembership hands over the spaces the fork had joined, skipping any the
// survivor is already in.
func (s *State) moveMembership(from, into string) int {
	n := 0
	for _, id := range sortedKeys(s.Spaces) {
		ch := s.Spaces[id]
		mem, ok := ch.Members[from]
		if !ok {
			continue
		}
		delete(ch.Members, from)
		if _, already := ch.Members[into]; !already {
			ch.Members[into] = mem
			n++
		}
	}
	return n
}

// checkMerge refuses a merge that would lose something, and it runs inside
// Apply rather than in Admit for the reason applyPrune's own checks do: Admit
// takes no State, and these questions are all about state.
//
// SAFE FOR REPLAY, and the reason is worth stating because the general rule
// says otherwise. Apply must accept everything it has ever accepted, or the
// daemon refuses its own ledger. merge_agents is a NEW op kind, so there are
// no historical ops for a new rule to be retroactive about; and a ledgered
// merge was applied against a state replay rebuilds, so the agents it names
// exist again at that point in the fold. If this is ever tightened further,
// ask that question again rather than assuming it.
//
// The SHAPE checks are in Admit, where shape checks belong.
func (s *State) checkMerge(op *Op) error {
	from, into := s.Agents[op.To], s.Agents[op.MergeInto]
	if from == nil {
		return errf("E_NO_AGENT", "check the id on the board", "no agent %q to merge", op.To)
	}
	if into == nil {
		return errf("E_NO_AGENT", "check the id on the board", "no agent %q to merge into", op.MergeInto)
	}
	if from.Status == StatusActive {
		return errf("E_AGENT_LIVE",
			"a running agent is not a duplicate of anything: let it sign off, or "+
				"evict it if it is stuck, and merge the row afterwards",
			"%q is active, and merging a live agent would redirect its mail "+
				"into another mailbox while it is still working", from.ID)
	}
	// BOTH HOLDING ONE PATH IS A CONFLICT AN ADMIN HAS TO SEE. Folding it
	// silently would drop a claim, and a claim that vanishes is how two
	// agents end up writing the same file.
	for _, c := range s.Claims {
		if c.Agent != from.ID {
			continue
		}
		if other := s.claimOn(c.Path, into.ID); other != nil {
			return errf("E_CLAIM_CONFLICT",
				"release one of them first: force_release takes a claim off a row "+
					"nobody is behind",
				"both %q and %q claim %q, so the merge would have to drop one",
				from.ID, into.ID, c.Path)
		}
	}
	return nil
}

// claimOn is the claim this agent holds on exactly this path, or nil.
func (s *State) claimOn(path, agent string) *Claim {
	for _, c := range s.Claims {
		if c.Agent == agent && c.Path == path {
			return c
		}
	}
	return nil
}

// sortedMessageKeys keeps the fold deterministic: ranging a map gives a
// different order every run, and the events below go into the ledger.
func sortedMessageKeys(m map[uint64]*Message) []uint64 {
	out := make([]uint64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
