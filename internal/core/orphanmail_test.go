package core

import (
	"testing"
	"time"
)

// A new agent must not inherit the previous occupant's mail.
//
// An id is derived from the name, so a name that comes back reuses the id, and
// mail outlives the row it was addressed to: a sweep written before v0.0.7
// deletes the row and KEEPS the messages, which its op records and replay must
// preserve. Those messages are expired with a reason the SENDER reads, so they
// are deliberately retained and there is a test for that. But they were still
// addressed to this id, so the next agent to take the name saw them.
//
// Measured before fixing: registering the same name after such a sweep showed
// the previous occupant's question verbatim, body and all. Found by the
// pre-release review, which said the purge could not reach this mail; it
// cannot, and deleting it would destroy the sender's record. The watermark is
// the mechanism that already exists for "mail below this is not mine".
func TestANewAgentDoesNotInheritTheLastOccupantsMail(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg(t, s, "sender", "tok-s", t0)
	reg(t, s, "target", "tok-t", t0)
	r := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgQuestion, Body: "SECRET may I proceed?", DeadlineSec: 60,
	}, now)
	ser, _ := r["msg_serial"].(uint64)

	// What a pre-v0.0.7 sweep leaves behind: the row gone, the mail kept.
	delete(s.Agents, "target")
	if _, _, err := s.Apply(&Op{Kind: OpSweep, PurgeMail: true}, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Setup must hold on both halves, or this asserts nothing.
	if s.Messages[ser] == nil {
		t.Fatal("setup: the message was deleted, so there is nothing to inherit " +
			"and the sender has lost the record it is kept for")
	}

	// The name comes back, on a build that has the repair. V7Semantics is what
	// says so: an op only gets v0.0.7's fold rules if it was written under them,
	// which is what keeps an older ledger replaying as itself.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "target", NewToken: "tok-t2", V7Semantics: true,
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}

	for _, m := range s.Inbox("target") {
		if m.Serial == ser {
			t.Errorf("a new agent of the same name was handed the previous "+
				"occupant's mail: #%d from %q, body %q", m.Serial, m.From, m.Body)
		}
	}

	// And the message itself survives, because the SENDER reads why nobody
	// answered. Deleting it would trade one silent loss for another.
	if m := s.Messages[ser]; m == nil {
		t.Error("the message was deleted; the sender's record of why nobody " +
			"answered is what keeps it")
	} else if m.ExpireDetail == "" {
		t.Error("the message survives but says nothing about why it expired")
	}
}

// And an ordinary agent still receives mail sent to it after it registers,
// which is the thing the watermark must not break.
func TestAnAgentStillReceivesItsOwnMail(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg(t, s, "sender", "tok-s", t0)
	reg(t, s, "target", "tok-t", t0)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgNotify, Body: "for you",
	}, now)
	if got := len(s.Inbox("target")); got != 1 {
		t.Errorf("an agent sees %d of the 1 message addressed to it after it "+
			"registered; the watermark has swallowed live mail", got)
	}
}
