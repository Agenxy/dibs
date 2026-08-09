package core

import (
	"testing"
	"time"
)

// A shared identifying ref matches even when the channel has no footprint.
//
// The candidate filter required a scorer footprint on the CHANNEL before it
// would compare refs, so a channel opened by an agent whose declaration
// predicted no files was invisible to ref matching. Two agents declaring
// issue:42, in the same repository, with the same activity, opened two separate
// channels — the exact duplication the product exists to prevent, failing in
// exactly the case a hand-written identifier is for: when the scorer has no
// opinion.
//
// The comment on that filter already said declared facts must work without a
// footprint. It was true of the caller's side and not of the channel's.
func TestASharedRefMatchesAChannelWithNoFootprint(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()

	reg := func(name string) string {
		_, _, err := s.Apply(&Op{
			Kind: OpRegisterLane, Name: name, NewToken: "tok-" + name, SessionID: name,
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

	// Open the channel, then declare the ref in a SLOT.
	//
	// A channel's refs come from its members' slots, not from the lane_open op, so
	// declaring the ref on the open alone exercises nothing. Auto-opening from a
	// declaration is the engine's job; at this layer the channel is created
	// explicitly and the owner then declares, which reaches the same state.
	if _, _, err := s.Apply(&Op{
		Kind: OpLaneOpen, Token: owner, Channel: "ticket-42", Text: "implement ticket",
	}, now); err != nil {
		t.Fatalf("lane_open: %v", err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpSetSlot, Token: owner, Text: "implement ticket",
		Refs: []string{"issue:42"},
	}, now); err != nil {
		t.Fatalf("owner set_slot: %v", err)
	}
	lane := s.Channels["ticket-42"]
	if lane == nil {
		t.Fatal("setup: the channel was not opened")
	}
	if len(lane.Predicted) != 0 {
		t.Skip("this fixture produced a footprint; the no-footprint path is what is " +
			"under test and a scorer change has made it unreachable here")
	}

	matches := s.MatchLanesEvidence("joiner", Slot{
		Text: "fix the same ticket", Refs: []string{"issue:42"},
	}, "", "", nil, nil, 10)

	found := false
	for _, m := range matches {
		if m.Lane == lane.ID {
			found = true
			if len(m.SharedRefs) == 0 {
				t.Errorf("matched %s but reported no shared refs: %+v", lane.ID, m)
			}
		}
	}
	if !found {
		t.Errorf("a channel sharing issue:42 was not matched because it had no scorer "+
			"footprint — two agents on one ticket would open two channels. Got %+v",
			matches)
	}
}
