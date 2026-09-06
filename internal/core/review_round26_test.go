package core

import (
	"testing"
	"time"
)

// R26: a notify to a full mailbox may displace only a notify that counts
// toward capacity; a predecessor's notify below the watermark frees nothing.
func TestDisplacementCannotSpendAPredecessorsInvisibleNotify(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxMailboxDepth = 2
	s := NewState("test", lim)
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "sender", NewToken: "tok-s", AgentKind: KindPersistent, Nonce: "ns", V7Semantics: true},
		{Kind: OpRegister, Name: "target", NewToken: "tok-t", AgentKind: KindPersistent, Nonce: "nt", V7Semantics: true},
		{Kind: OpAckBoard, Token: "tok-s"},
		{Kind: OpAckBoard, Token: "tok-t"},
		// the previous occupant's notify, left behind by an old sweep
		{Kind: OpSendMessage, Token: "tok-s", To: "target", MsgType: MsgNotify, Body: "old notify", V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	delete(s.Agents, "target") // the row goes, the mail stays
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "target", NewToken: "tok-t2", AgentKind: KindPersistent, Nonce: "nt2", V7Semantics: true}, t0.Add(time.Hour)); err != nil {
		t.Fatal("setup:", err)
	}
	if got := s.Inbox("target"); len(got) != 0 {
		t.Fatalf("setup: the replacement sees %d message(s); the predecessor's notify must be fenced", len(got))
	}
	for _, body := range []string{"q1", "q2"} {
		if _, _, err := s.Apply(&Op{Kind: OpSendMessage, Token: "tok-s", To: "target", MsgType: MsgQuestion, Body: body, DeadlineSec: 60, V7Semantics: true}, t0.Add(2*time.Hour)); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if n := nonTerminalCount(s, "target"); n != 2 {
		t.Fatalf("setup: the visible mailbox holds %d, want the cap of 2", n)
	}
	_, _, err := s.Apply(&Op{Kind: OpSendMessage, Token: "tok-s", To: "target", MsgType: MsgNotify, Body: "one more", V7Semantics: true}, t0.Add(3*time.Hour))
	if err == nil {
		t.Fatal("a notify to a full mailbox landed by displacing the predecessor's invisible notify: " +
			"it freed no counted slot, and the cap is exceeded by one for every such notify")
	}
	if n := nonTerminalCount(s, "target"); n != 2 {
		t.Errorf("the visible mailbox holds %d after the refused send, want 2", n)
	}
}
