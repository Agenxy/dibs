package core

import (
	"slices"
	"testing"
)

// There was no way out of an exclusive space's queue.
//
// leave_space checked membership, found none, and answered "not a member": true
// and useless, because the agent was in the queue and stayed there. An
// exclusive space admits from its queue whenever it frees, so an agent that
// queued, changed its mind, and was told it had left, was joined to the agent
// minutes later: handed the coordination key and made liable for acknowledging
// announcements in work it had explicitly declined.
func TestAQueuedAgentCanActuallyLeaveTheQueue(t *testing.T) {
	st := NewState("test", DefaultLimits())
	now := t0
	reg := func(name string) string {
		tok := "tok-" + name
		if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: name, NewToken: tok}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return tok
	}
	holder, quitter, stayer := reg("holder"), reg("quitter"), reg("stayer")

	if _, _, err := st.Apply(&Op{
		Kind: OpSpaceOpen, Token: holder, Space: "excl", Text: "t", Exclusive: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{quitter, stayer} {
		if _, _, err := st.Apply(&Op{
			Kind: OpSpaceJoin, Token: tok, Space: "excl", Score: 0.9, ScorerID: "t",
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	ch := st.Spaces["excl"]
	if len(ch.Queue) != 2 {
		t.Fatalf("precondition: expected 2 queued, got %v", ch.Queue)
	}

	res, _, err := st.Apply(&Op{Kind: OpSpaceLeave, Token: quitter, Space: "excl"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res["left"] != true {
		t.Fatalf("a queued agent was told it had not left: %v", res)
	}
	if slices.Contains(ch.Queue, "quitter") {
		t.Fatalf("quitter is still in the queue: %v", ch.Queue)
	}
	if _, still := ch.Pending["quitter"]; still {
		t.Error("quitter's pending membership outlived its departure")
	}

	// The agent frees. The one that stayed gets in; the one that left does not.
	if _, _, err := st.Apply(&Op{
		Kind: OpSpaceExclusive, Token: holder, Space: "excl", Mode: "release",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, in := ch.Members["quitter"]; in {
		t.Error("an agent that left the queue was admitted to the agent anyway. " +
			"with the coordination key and every announcement it now owes")
	}
	if _, in := ch.Members["stayer"]; !in {
		t.Errorf("the agent that kept waiting was not admitted; members: %v", ch.Members)
	}
}
