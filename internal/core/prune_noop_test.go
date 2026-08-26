package core

import "testing"

// A prune that closes nothing must not be written down.
//
// "An op is ledgered iff it changed replayable state" is the rule this
// repository states about itself, and the engine ledgers exactly when the
// serial advanced. An all-agent prune with no debris to close built no targets
// and called finish anyway, so an idle prune appended an op recording that
// nothing happened, forever, on demand. Found by the pre-release review.
func TestAPruneWithNoDebrisIsNotLedgered(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "busy", "tok-busy", t0) // active, so never a target
	before := s.Serial

	// V7Semantics: this asserts v0.0.7's rules, and an op only gets them
	// if it records that it was written under them.
	res := mustApply(t, s, &Op{Kind: OpPrune, V7Semantics: true}, t0)

	if n, _ := res["count"].(int); n != 0 {
		t.Fatalf("setup: pruned %d agent(s); this case is about pruning none", n)
	}
	if s.Serial != before {
		t.Errorf("an idle prune advanced the serial (%d to %d), so the engine "+
			"appends an op saying nothing happened, every time somebody asks",
			before, s.Serial)
	}
}

// And pruning an agent that is already closed must not close it twice.
//
// The named-prune path did not check status, so it ran the close again and
// emitted a second `agent.closed` for an agent that closed once. The audit
// stream is what `dibs log` and every events_since consumer reads, and a
// transition that never happened is worse there than a missing one because it
// is indistinguishable from a real one.
func TestPruningAClosedAgentDoesNotCloseItAgain(t *testing.T) {
	s := NewState("t", DefaultLimits())
	a := reg(t, s, "done", "tok-done", t0)
	mustApply(t, s, &Op{Kind: OpSignOff, Token: a.Token}, t0)
	if s.Agents["done"].Status != StatusClosed {
		t.Fatal("setup: the agent is not closed, so this tests nothing")
	}
	before := s.Serial

	res, evs, err := s.Apply(&Op{Kind: OpPrune, To: "done", V7Semantics: true}, t0)
	if err != nil {
		t.Fatalf("pruning an already-closed agent was refused: %v. A refusal in "+
			"the fold is retroactive and would stop a daemon replaying its own "+
			"ledger; doing nothing is the answer", err)
	}
	for _, e := range evs {
		if e.Type == "agent.closed" {
			t.Error("a second agent.closed was emitted for an agent that closed " +
				"once, so the audit stream claims a transition that never happened")
		}
	}
	if s.Serial != before {
		t.Errorf("the serial advanced (%d to %d) for a prune that changed nothing",
			before, s.Serial)
	}
	if n, _ := res["count"].(int); n != 0 {
		t.Errorf("reported %d pruned; nothing was", n)
	}
}

// prune_own must not re-close a closed agent either.
//
// The admin path was fixed a round earlier and this one was not, which is the
// shape of half the defects in this cycle: a rule applied at one of two doors.
// applyPruneOwn rejects an ACTIVE target and let a CLOSED one fall through to
// applyClose, which emits agent.closed unconditionally, and to finish, which
// advances the serial. The audit stream then claims a transition that never
// happened. Found by the pre-release review, which also noted the changelog
// described only the admin half as repaired.
func TestPruneOwnDoesNotRecloseAClosedAgent(t *testing.T) {
	s := NewState("t", DefaultLimits())
	// A PARENT tidying a finished child, which is the case this op exists for.
	// The child cannot be the actor: sign_off blanks its token, and a closed
	// agent cannot call at all.
	reg(t, s, "parent", "tok-parent", t0)
	reg(t, s, "child", "tok-child", t0)
	s.Agents["child"].Parent = "parent"
	s.Agents["child"].ParentProven = true
	s.Agents["child"].Status = StatusClosed
	before := s.Serial

	_, evs, err := s.Apply(&Op{
		Kind: OpPruneOwn, Token: "tok-parent", To: "child", V7Semantics: true,
	}, t0)
	if err != nil {
		// A refusal in the fold would be retroactive; doing nothing is the answer.
		t.Fatalf("prune_own of an already-closed agent was refused: %v", err)
	}
	for _, e := range evs {
		if e.Type == "agent.closed" {
			t.Error("a second agent.closed was emitted for an agent that closed once")
		}
	}
	if s.Serial != before {
		t.Errorf("the serial advanced (%d to %d) for a prune that changed nothing",
			before, s.Serial)
	}
}
