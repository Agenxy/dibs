package core

import (
	"testing"
	"time"
)

// An agent that walks out of an agent and is put straight back has not been
// coordinated with: it has been overruled.
//
// Reported from a live fleet by an agent that left an agent it did not belong in
// and posted its reasons on the way out: "my very next declare auto-joined me
// again, score UP from 0.1651 to 0.2289, same generic evidence."
func TestLeavingALaneStopsItAutoJoiningYouAgain(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	reg(t, s, "builder", "t-builder", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-builder"}, t0)

	agent(t, s, "acme", "acme fleet", []string{"Justfile", "src/main.go"})
	s.Spaces["acme"].Members["builder"] = &Membership{}

	res := mustApply(t, s, &Op{Kind: OpSpaceLeave, Token: "t-builder", Space: "acme"}, t0)
	if res["left"] != true {
		t.Fatalf("setup: leave failed: %v", res)
	}

	// Declaring work that still scores against that agent must surface it and NOT
	// re-join. Surfacing matters: the agent is allowed to change its mind, and a
	// second checkout or a genuine overlap is real.
	got := s.MatchAgentsWith("builder", fp("Justfile", "src/main.go"), nil, 5)
	if len(got) != 1 {
		t.Fatalf("the agent must still be surfaced, got %d matches", len(got))
	}
	if !got[0].Declined {
		t.Error("a deliberate departure must be remembered, or auto-join undoes it")
	}
}

// Eviction is somebody else's decision, and a sweep removing a crashed agent is
// not a decision at all. Neither may quietly disqualify that agent from ever
// being matched here again.
func TestOnlyADeliberateLeaveCounts(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	reg(t, s, "worker", "t-w", t0)

	agent(t, s, "auth", "auth", []string{"internal/auth/token.go"})
	ch := s.Spaces["auth"]
	ch.Members["worker"] = &Membership{}

	// The path the sweep and evict both take.
	s.departChannel(ch, "worker")

	if ch.Declined["worker"] {
		t.Error("being removed by someone else is not a decision this agent made")
	}
	got := s.MatchAgentsWith("worker", fp("internal/auth/token.go"), nil, 5)
	if len(got) != 1 || got[0].Declined {
		t.Errorf("an evicted or swept agent must remain auto-joinable: %+v", got)
	}
}
