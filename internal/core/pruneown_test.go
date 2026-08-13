package core

import "testing"

// An agent may tidy up after itself, and after children it vouched for.
//
// The board had no way for an agent to remove a record it created: prune_lane
// exists but is admin-only, so an agent that spawned three subagents left three
// rows behind it forever and could only ask a human to clear them. That is a
// poor fit for a tool whose whole claim is that agents drive it.
func TestAnAgentCanPruneItsOwnVouchedChild(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	parent := reg(t, s, "parent", "tok-parent", t0)
	res := spawnChild(t, s, parent.Token, parent.ID, "nonce-child-0123456789abcdef")
	childID := res["agent_id"].(string)

	// Finished first: prune tidies a record, it does not stop a worker.
	mustApply(t, s, &Op{Kind: OpSignOff, Token: "tok-helper"}, t0)

	got := mustApply(t, s, &Op{Kind: OpPruneOwn, Token: parent.Token, To: childID}, t0)
	if got["pruned"] != childID {
		t.Errorf("pruned %v, want %s", got["pruned"], childID)
	}
}

// A peer is not debris.
//
// This restriction is the point of the feature rather than a caveat on it. An
// agent that can prune peers can delete the row that would have told it
// somebody else is already pursuing its objective, which is the single thing
// this board exists to show. The alarm must not be switchable off from inside.
func TestAnAgentCannotPruneAPeer(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	mine := reg(t, s, "mine", "tok-mine", t0)
	other := reg(t, s, "other", "tok-other", t0)
	mustApply(t, s, &Op{Kind: OpSignOff, Token: other.Token}, t0)

	if _, _, err := s.Apply(&Op{Kind: OpPruneOwn, Token: mine.Token, To: other.ID}, t0); err == nil {
		t.Fatal("an agent pruned a peer: it can now remove the evidence of duplicated work")
	}
	if s.Agents[other.ID] == nil {
		t.Error("the peer's record disappeared despite the refusal")
	}
}

// An unvouched child is a peer. Parent arrives as a bare string on the wire and
// proves nothing on its own, which is exactly why ParentProven exists.
func TestAnUnvouchedChildIsNotPrunable(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	parent := reg(t, s, "parent", "tok-parent", t0)
	// Claims the parent without a vouching nonce: anybody can say this.
	res := mustApply(t, s, &Op{
		Kind: OpRegister, Name: "claimed", NewToken: "tok-claimed", Parent: parent.ID,
	}, t0)
	childID := res["agent_id"].(string)
	mustApply(t, s, &Op{Kind: OpSignOff, Token: "tok-claimed"}, t0)

	if _, _, err := s.Apply(&Op{Kind: OpPruneOwn, Token: parent.Token, To: childID}, t0); err == nil {
		t.Error("a merely CLAIMED parent pruned a child; anyone can name a parent on the wire")
	}
}

// Pruning a working agent would release its claims and blank its token
// underneath it, which is coercion. sign_off is how an agent stops.
func TestPruningAnActiveAgentIsRefused(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	parent := reg(t, s, "parent", "tok-parent", t0)
	res := spawnChild(t, s, parent.Token, parent.ID, "nonce-child-0123456789abcdef")
	childID := res["agent_id"].(string)

	if _, _, err := s.Apply(&Op{Kind: OpPruneOwn, Token: parent.Token, To: childID}, t0); err == nil {
		t.Fatal("pruned an ACTIVE agent: its claims would be released while it is working")
	}
	if got := s.Agents[childID].Status; got != StatusActive {
		t.Errorf("the active child's status changed to %v", got)
	}
}
