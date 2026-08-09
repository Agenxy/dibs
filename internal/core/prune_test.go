package core

import (
	"testing"
	"time"
)

// A crashed lane cannot close itself — close_lane needs the lane's own token,
// which a dead lane no longer has. Without prune the board accumulates debris
// nobody can clear.
func TestPruneClearsDebrisButNeverLiveLanes(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	for _, n := range []string{"live", "gone-a", "gone-b"} {
		if _, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: n, NewToken: "tok-" + n}, now); err != nil {
			t.Fatal(err)
		}
	}
	// Two lanes lose coordination; one keeps working.
	s.Lanes["gone-a"].Status = StatusStale
	s.Lanes["gone-b"].Status = StatusDormant

	res, _, err := s.Apply(&Op{Kind: OpPruneLane}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res["count"] != 2 {
		t.Fatalf("pruned %v lanes, want 2", res["count"])
	}
	if s.Lanes["live"].Status != StatusActive {
		t.Error("prune closed a lane that was still working")
	}
	for _, id := range []string{"gone-a", "gone-b"} {
		if s.Lanes[id].Status != StatusClosed {
			t.Errorf("%s not closed", id)
		}
		if s.Lanes[id].Token != "" {
			t.Errorf("%s kept its token after closing", id)
		}
	}
}

// Naming a lane prunes exactly that one, live or not — the human said so.
func TestPruneNamedLaneAndUnknownLane(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	_, _, _ = s.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: "t1"}, now)
	_, _, _ = s.Apply(&Op{Kind: OpRegisterLane, Name: "b", NewToken: "t2"}, now)

	if _, _, err := s.Apply(&Op{Kind: OpPruneLane, To: "a"}, now); err != nil {
		t.Fatal(err)
	}
	if s.Lanes["a"].Status != StatusClosed {
		t.Error("named lane not closed")
	}
	if s.Lanes["b"].Status != StatusActive {
		t.Error("prune of one lane touched another")
	}
	if _, _, err := s.Apply(&Op{Kind: OpPruneLane, To: "nope"}, now); err == nil {
		t.Error("pruning an unknown lane should error, not succeed silently")
	}
}
