package core

import (
	"strings"
	"testing"
	"time"
)

// The two calls that return an agent's mail disagreed about what to call it, and
// each used the OTHER one's name: the inbox tool returned `messages`, check_in
// returned `inbox`.
//
// So an agent that called inbox and read the inbox key got an empty list while
// its mail sat one key away, with nothing anywhere saying so. Found from the
// outside on a day spent fixing ways mail goes missing: it cost a debugging
// cycle to diagnose, and an agent would simply have read "no mail" and moved on.
//
// Both names now carry the same mail everywhere. Aliased rather than renamed:
// each key is what one of the calls has always returned, and something is
// reading it.
func TestBothMailKeysCarryTheMail(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	reg(t, s, "sender", "t-send", t0)
	reg(t, s, "reader", "t-read", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-send"}, t0)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "t-send", To: "reader", MsgType: MsgNotify, Body: "do not lose me",
	}, t0)

	res := mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-read"}, t0)

	under := func(key string) int {
		v, ok := res[key]
		if !ok {
			t.Fatalf("check_in has no %q key; an agent reading it sees no mail", key)
		}
		msgs, ok := v.([]*Message)
		if !ok {
			t.Fatalf("%q = %T, want a message list", key, v)
		}
		return len(msgs)
	}
	if n := under("inbox"); n != 1 {
		t.Errorf(`check_in["inbox"] = %d, want 1`, n)
	}
	if n := under("messages"); n != 1 {
		t.Errorf(`check_in["messages"] = %d, want 1: the inbox tool's name for the same thing`, n)
	}
}

// An agent could not tell whether a message woke it or had been sitting in a
// queue until it happened to look. Serials order events; they do not measure
// waiting. Asked for by an agent reached mid-restart: "put delivered_at and
// read_at in the envelope and the gap answers it."
//
// DeliveredTime IS the read time: a message becomes delivered when its
// recipient pulls it, so the gap from SentAt is exactly how long it waited.
func TestEnvelopeCarriesSentAndDeliveredTimes(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	reg(t, s, "sender", "t-send", t0)
	reg(t, s, "reader", "t-read", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-send"}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "t-send", To: "reader", MsgType: MsgNotify, Body: "hello",
	}, t0)
	serial := res["msg_serial"].(uint64)

	if got := s.Messages[serial].SentAt; !got.Equal(t0) {
		t.Errorf("sent_at = %v, want the send time %v", got, t0)
	}
	if !s.Messages[serial].DeliveredTime.IsZero() {
		t.Error("delivered_at must be empty until somebody actually pulls it")
	}

	// The recipient looks, five minutes later.
	read := t0.Add(5 * time.Minute)
	mustApply(t, s, &Op{Kind: OpMarkDelivered, MsgSerials: []uint64{serial}}, read)

	m := s.Messages[serial]
	if !m.DeliveredTime.Equal(read) {
		t.Fatalf("delivered_at = %v, want the moment it was pulled %v", m.DeliveredTime, read)
	}
	if waited := m.DeliveredTime.Sub(m.SentAt); waited != 5*time.Minute {
		t.Errorf("the gap must measure the wait: got %v, want 5m", waited)
	}
}

// A misaddressed message must be fixable in one step.
//
// "check the board for live agents" is advice the agent has to act on with another
// call, and an agent that addressed an agent by the wrong name gave up instead of
// guessing which of the live agents was meant. It already told us who it wanted.
func TestMisaddressedMailNamesTheCandidates(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	t0 := time.Now()
	reg(t, s, "claude-orchestrator", "t-orch", t0)
	reg(t, s, "sender", "t-send", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t-send"}, t0)

	_, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "t-send", To: "claude", MsgType: MsgNotify, Body: "hi",
	}, t0)
	if err == nil {
		t.Fatal("want a refusal for an unknown agent")
	}
	var ce *Error
	if !asErr(err, &ce) {
		t.Fatalf("want a core error, got %T", err)
	}
	if !strings.Contains(ce.Hint, "claude-orchestrator") {
		t.Errorf("the hint must name the agent the agent probably meant, got: %s", ce.Hint)
	}
}
