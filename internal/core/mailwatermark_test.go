package core

import (
	"testing"
	"time"
)

// Mail the recipient cannot see does not fill its mailbox.
//
// TruncatedBefore says "mail below this is not mine": an id is derived from the
// name, so a name that comes back reuses the id, and a sweep written before
// v0.0.7 removes the agent row while keeping the messages. Inbox filters on it.
// The CAPACITY metric did not, so mail addressed to a previous occupant counted
// against the current one, and a send could be refused with E_MAILBOX_FULL
// against an agent whose inbox reads as empty.
//
// Nothing could clear it. The recipient cannot see the mail, so it cannot read,
// answer, ack or consume it; the sender is simply refused. Rule 6 says every
// error names the corrective call, and this one had none to name. Only a notify
// may displace, so a question to that agent was refused permanently.
//
// Found by sweeping for the class the round fifty-seven review turned up: a
// query by `To == id` that does not ask whose mail it is.
func TestInvisibleMailDoesNotFillTheMailbox(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxMailboxDepth = 2
	s := NewState("probe", lim)
	now := time.Unix(1700000000, 0)
	reg(t, s, "sender", "tok-s", now)
	reg(t, s, "target", "tok-old", now)

	for i := 0; i < 2; i++ {
		mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "tok-s", To: "target",
			MsgType: MsgNotify, Body: "FOR THE PREVIOUS OCCUPANT",
		}, now)
	}
	// Non-terminal, or they would not count against capacity either way and
	// this fixture would prove nothing.
	for _, m := range s.Messages {
		if m.To == "target" && m.Terminal() {
			t.Fatalf("setup: message %d is terminal", m.Serial)
		}
	}

	delete(s.Agents, "target")
	mustApply(t, s, &Op{
		Kind: OpRegister, Name: "target", NewToken: "tok-new", V7Semantics: true,
	}, now.Add(3*time.Hour))
	if got := s.Inbox("target"); len(got) != 0 {
		t.Fatalf("setup: the returning agent must see nothing, saw %d", len(got))
	}

	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgQuestion, Body: "CAN YOU HEAR ME", DeadlineSec: 60,
	}, now.Add(4*time.Hour)); err != nil {
		t.Fatalf("send refused with %v against an agent whose visible inbox is empty. "+
			"Neither party can clear mail the recipient cannot see", err)
	}

	// And the capacity rule still WORKS, or this passes against a metric that
	// counts nothing at all. One more fills it: the send above already took a
	// slot, which the first draft of this test forgot and so failed against its
	// own fix.
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgQuestion, Body: "MINE", DeadlineSec: 60,
	}, now.Add(5*time.Hour))
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgQuestion, Body: "OVER THE LIMIT", DeadlineSec: 60,
	}, now.Add(6*time.Hour)); err == nil {
		t.Error("the mailbox never fills: excluding a previous occupant's mail " +
			"must not turn the capacity limit off")
	}
}
