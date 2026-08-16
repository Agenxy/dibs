package core

import (
	"slices"
	"strings"
	"testing"
)

// The answers a question offers are part of what was asked.
//
// They are on the MESSAGE and therefore in the ledger, which is the decision
// worth pinning: the alternative is to treat them as a delivery detail of the
// notification that raised them, which is cheaper and wrong. A question replayed
// without its options is a different question, its recipient is asked something
// nobody wrote, and the board renders an answer space that does not exist.
func TestAQuestionKeepsTheAnswersItOffered(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)

	want := []string{"rebase", "merge", "leave it"}
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgQuestion,
		Body: "how should I land this?", Choices: want,
	}, t0)
	ser := res["msg_serial"].(uint64)

	if got := s.Messages[ser].Choices; !slices.Equal(got, want) {
		t.Errorf("the message records choices %v, want %v: the recipient is being asked "+
			"something other than what was sent", got, want)
	}
}

// The bound is at INGRESS, which is the rule this codebase keeps relearning.
//
// Admit runs on the way in; Apply is the fold and replays ops accepted by older
// code. A limit enforced in Apply is retroactive: the daemon meets a ledger it
// wrote yesterday, refuses its own record, and does not boot. So this asserts
// Admit rejects and says nothing about Apply.
func TestTooManyChoicesAreRejectedAtIngress(t *testing.T) {
	lim := DefaultLimits()
	over := make([]string, MaxChoices+1)
	for i := range over {
		over[i] = "opt"
	}
	if err := Admit(&Op{Kind: OpSendMessage, MsgType: MsgQuestion, Choices: over}, lim); err == nil {
		t.Errorf("Admit accepted %d choices with a ceiling of %d: a question long enough "+
			"to need scrolling has given up the thing stating them was for",
			len(over), MaxChoices)
	}
	// A choice is a button label. Unbounded, it is permanent ledger: this is the
	// same shape as the 2 MiB session_id a probe once pushed through.
	huge := strings.Repeat("x", lim.MaxNameBytes+1)
	if err := Admit(&Op{Kind: OpSendMessage, MsgType: MsgQuestion, Choices: []string{huge}}, lim); err == nil {
		t.Errorf("Admit accepted a %d-byte choice with a %d-byte ceiling: it is a button "+
			"label, and it is re-read into memory on every start forever",
			len(huge), lim.MaxNameBytes)
	}
	// And the honest case still passes, or the bound is just an outage.
	if err := Admit(&Op{
		Kind: OpSendMessage, MsgType: MsgQuestion, Choices: []string{"yes", "no"},
	}, lim); err != nil {
		t.Errorf("Admit rejected two ordinary choices: %v", err)
	}
}
