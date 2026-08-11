package core

import (
	"strings"
	"testing"
)

// A cleared slot must not make the next declaration overwrite a live one.
//
// Ids were generated as "s" + (len+1). With s1, s2, s3 and s2 cleared, len is 2
// and the next id is "s3", which already exists, so a new declaration silently
// replaced a different piece of work, and the per-agent limit check waved it
// through because the id was not new. Nothing errored and nothing was logged:
// an agent declared what it was doing and quietly erased what it had been.
func TestClearingASlotDoesNotMakeTheNextOneOverwriteAnother(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "builder", "tokB", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tokB"}, t0)

	for _, text := range []string{"first", "second", "third"} {
		mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokB", Text: text}, t0)
	}
	mustApply(t, s, &Op{Kind: OpClearSlot, Token: "tokB", SlotID: "s2"}, t0)
	mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokB", Text: "fourth"}, t0)

	slots := s.Agents["builder"].Slots
	if len(slots) != 3 {
		t.Fatalf("expected 3 slots after three sets, a clear and a set; got %d: %v", len(slots), slots)
	}
	if got := slots["s3"].Text; got != "third" {
		t.Errorf("s3 now reads %q: the new declaration took an id that was still in use\n"+
			"  and overwrote live work rather than picking a free one", got)
	}
	found := false
	for _, sl := range slots {
		if sl.Text == "fourth" {
			found = true
		}
	}
	if !found {
		t.Error("the fourth declaration is not on the board at all")
	}
}

// An agent updating its focus calls declare again with new text, which is
// exactly what the tool's own description invites, and that MINTS a slot every
// time, so an agent that is simply working stacks declarations until it hits the
// cap and starts erroring.
//
// Dibs cannot know whether a second slot was intended, so it does not guess.
// It says what it did and what to pass instead: told, not prevented, which is
// the rule the rest of the board follows.
func TestAddingASecondSlotSaysSo(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "builder", "tokB", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tokB"}, t0)

	res := mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokB", Text: "first"}, t0)
	if res["note"] != nil {
		t.Errorf("the FIRST declaration was flagged as growth: %v", res["note"])
	}

	res = mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokB", Text: "second"}, t0)
	note, _ := res["note"].(string)
	if !strings.Contains(note, "slot_id") {
		t.Errorf("adding a second slot said %q: an agent updating its focus has no way\n"+
			"  to learn it is stacking declarations rather than replacing one", note)
	}

	// Updating in place is the intended path and must stay quiet, or the advice
	// becomes noise on every well-formed call.
	res = mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokB", SlotID: "s1", Text: "revised"}, t0)
	if res["note"] != nil {
		t.Errorf("updating an existing slot was flagged: %v", res["note"])
	}
}
