package core

import (
	"strings"
	"testing"
)

// Mail from a sender you cannot answer must say so when you READ it.
//
// Reported from a live board. A message arrived through an adoption, from an
// agent that no longer existed. Replying returned E_NO_AGENT with a helpful
// suggestion of who to try instead, so the board knew the answer; nothing in
// the inbox said it, so the only way to find out was to try.
//
// It matters most for exactly the mail adoption recovers: inherited mail is old
// by definition, so its senders are the likeliest rows to have evaporated. The
// feature that rescues stranded mail is the one that most reliably hands you
// mail you cannot answer. In the reported case the only correct reply was to
// tell the sender the desk had changed hands.
//
// THIS COVERS check_in ONLY, and it used to claim otherwise.
//
// It said "BOTH READ PATHS are asserted". Every assertion below reads the
// result of OpAckBoard, which is the fold's half; the inbox TOOL is separate
// code in internal/engine. Deleting the engine assignment left this test green,
// verified by doing it, so the sentence was worse than no sentence: it told a
// reader the second door was guarded when nothing touched it. The same
// two-doors mistake as the adoption note, this time in the comment written to
// warn about the adoption note.
//
// The other door is TestTheInboxToolAlsoNamesUnanswerableSenders, in the engine
// package, where it has to be. Found by the pre-release review.
func TestTheInboxSaysWhenASenderCannotBeAnswered(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "reader", "tok-reader", t0)
	reg(t, s, "ghost", "tok-ghost", t0)
	reg(t, s, "alive", "tok-alive", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-reader"}, t0)

	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-ghost", To: "reader",
		MsgType: MsgNotify, Body: "thanks for the handover",
	}, t0)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-alive", To: "reader",
		MsgType: MsgNotify, Body: "still here",
	}, t0)

	// The sender goes away, the way the reported one had.
	s.Agents["ghost"].Status = StatusArchived

	// Setup must hold: the send path must really refuse a reply to the ghost,
	// or the inbox has nothing to warn about and this test proves nothing.
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-reader", To: "ghost",
		MsgType: MsgNotify, Body: "you are welcome",
	}, t0); err == nil {
		t.Fatal("setup: a reply to the archived sender succeeded, so there is no " +
			"unanswerable sender here to report")
	}

	res := mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-reader"}, t0)
	gone, _ := res["unanswerable_senders"].([]Result)
	if len(gone) != 1 {
		t.Fatalf("check_in reported %d unanswerable sender(s), wanted exactly the "+
			"archived one: %v", len(gone), res["unanswerable_senders"])
	}
	if gone[0]["from"] != "ghost" {
		t.Errorf("the wrong sender was named: %v", gone[0])
	}
	// The hint has to be the one the send path gives, not a second opinion.
	hint, _ := gone[0]["hint"].(string)
	if !strings.Contains(hint, "ghost") {
		t.Errorf("the hint does not name the sender it is about: %q", hint)
	}
	if hint != nearestAgentsHint(s, "ghost") {
		t.Errorf("the inbox hint and the send hint disagree, which is two answers to "+
			"one question:\n  inbox: %s\n  send:  %s", hint, nearestAgentsHint(s, "ghost"))
	}
}

// And a mailbox whose senders are all still there says nothing at all.
//
// The key is absent rather than empty on the overwhelmingly common path,
// because a warning that is always present is a warning nobody reads: the same
// habituation this release fixed in the waiting nudge.
func TestAnOrdinaryInboxCarriesNoSuchWarning(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "reader", "tok-reader", t0)
	reg(t, s, "alive", "tok-alive", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-reader"}, t0)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-alive", To: "reader",
		MsgType: MsgNotify, Body: "still here",
	}, t0)

	res := mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-reader"}, t0)
	if _, present := res["unanswerable_senders"]; present {
		t.Errorf("an inbox whose senders are all live still carried the warning: %v",
			res["unanswerable_senders"])
	}
}
