package core

import (
	"testing"
	"time"
)

// A shared identifying ref matches even when the space has no footprint.
//
// The candidate filter required a scorer footprint on the CHANNEL before it
// would compare refs, so a space opened by an agent whose declaration
// predicted no files was invisible to ref matching. Two agents declaring
// issue:42, in the same repository, with the same activity, opened two separate
// spaces: the exact duplication the product exists to prevent, failing in
// exactly the case a hand-written identifier is for: when the scorer has no
// opinion.
//
// The comment on that filter already said declared facts must work without a
// footprint. It was true of the caller's side and not of the space's.
func TestASharedRefMatchesAChannelWithNoFootprint(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()

	reg := func(name string) string {
		_, _, err := s.Apply(&Op{
			Kind: OpRegister, Name: name, NewToken: "tok-" + name, SessionID: name,
		}, now)
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok-" + name}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return "tok-" + name
	}
	owner := reg("owner")
	reg("joiner")

	// Open the space, then declare the ref in a SLOT.
	//
	// A space's refs come from its members' slots, not from the open_space op, so
	// declaring the ref on the open alone exercises nothing. Auto-opening from a
	// declaration is the engine's job; at this layer the space is created
	// explicitly and the owner then declares, which reaches the same state.
	if _, _, err := s.Apply(&Op{
		Kind: OpSpaceOpen, Token: owner, Space: "ticket-42", Text: "implement ticket",
	}, now); err != nil {
		t.Fatalf("open_space: %v", err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSetSlot, Token: owner, Text: "implement ticket",
		Refs: []string{"issue:42"},
	}, now); err != nil {
		t.Fatalf("owner declare: %v", err)
	}
	agent := s.Spaces["ticket-42"]
	if agent == nil {
		t.Fatal("setup: the space was not opened")
	}
	if len(agent.Predicted) != 0 {
		t.Skip("this fixture produced a footprint; the no-footprint path is what is " +
			"under test and a scorer change has made it unreachable here")
	}

	matches := s.MatchAgentsEvidence("joiner", Slot{
		Text: "fix the same ticket", Refs: []string{"issue:42"},
	}, "", "", nil, nil, 10)

	found := false
	for _, m := range matches {
		if m.Agent == agent.ID {
			found = true
			if len(m.SharedRefs) == 0 {
				t.Errorf("matched %s but reported no shared refs: %+v", agent.ID, m)
			}
		}
	}
	if !found {
		t.Errorf("a space sharing issue:42 was not matched because it had no scorer "+
			"footprint: two agents on one ticket would open two spaces. Got %+v",
			matches)
	}
}
