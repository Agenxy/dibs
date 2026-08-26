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

	res := mustApply(t, s, &Op{Kind: OpPrune}, t0)

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

	res, evs, err := s.Apply(&Op{Kind: OpPrune, To: "done"}, t0)
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
