package core

import (
	"testing"
	"time"
)

// A crashed agent cannot close itself: sign_off needs the agent's own token,
// which a dead agent no longer has. Without prune the board accumulates debris
// nobody can clear.
func TestPruneClearsDebrisButNeverLiveSpaces(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	for _, n := range []string{"live", "gone-a", "gone-b"} {
		if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: n, NewToken: "tok-" + n}, now); err != nil {
			t.Fatal(err)
		}
	}
	// Two agents lose coordination; one keeps working.
	s.Agents["gone-a"].Status = StatusStale
	s.Agents["gone-b"].Status = StatusDormant

	res, _, err := s.Apply(&Op{Kind: OpPrune}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res["count"] != 2 {
		t.Fatalf("pruned %v agents, want 2", res["count"])
	}
	if s.Agents["live"].Status != StatusActive {
		t.Error("prune closed an agent that was still working")
	}
	for _, id := range []string{"gone-a", "gone-b"} {
		if s.Agents[id].Status != StatusClosed {
			t.Errorf("%s not closed", id)
		}
		if s.Agents[id].Token != "" {
			t.Errorf("%s kept its token after closing", id)
		}
	}
}

// Naming an agent prunes exactly that one, live or not: the human said so.
func TestPruneNamedSpaceAndUnknownLane(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	_, _, _ = s.Apply(&Op{Kind: OpRegister, Name: "a", NewToken: "t1"}, now)
	_, _, _ = s.Apply(&Op{Kind: OpRegister, Name: "b", NewToken: "t2"}, now)

	if _, _, err := s.Apply(&Op{Kind: OpPrune, To: "a"}, now); err != nil {
		t.Fatal(err)
	}
	if s.Agents["a"].Status != StatusClosed {
		t.Error("named agent not closed")
	}
	if s.Agents["b"].Status != StatusActive {
		t.Error("prune of one agent touched another")
	}
	if _, _, err := s.Apply(&Op{Kind: OpPrune, To: "nope"}, now); err == nil {
		t.Error("pruning an unknown agent should error, not succeed silently")
	}
}
