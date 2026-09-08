package core

import (
	"testing"
	"time"
)

// F1: an op written before v0.0.7 reattaches by the rule it was written under.
//
// The fold began matching aliases and dormant rows. An op in a v0.0.6 ledger
// that found no primary-id match created a sibling, and every later op in that
// ledger names the sibling; replaying it with the wider match reattaches the
// original, the sibling never exists, and the next op that authenticates as it
// fails. The daemon refuses its own history. Found by the pre-release review.
func TestAPreV7RegisterKeepsThePrimaryOnlyReattachRule(t *testing.T) {
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	mk := func(status AgentStatus, primary string) *State {
		s := NewState("test", DefaultLimits())
		l := &Agent{
			ID: "worker", Name: "worker", Status: status, SessionID: primary,
			Nonce: "n", NonceMinted: true, Slots: map[string]Slot{},
		}
		if primary != thread {
			l.SessionAliases = []string{thread}
		}
		s.Agents["worker"] = l
		return s
	}
	old := &Op{Kind: OpRegister, Name: "worker", SessionID: thread, NewToken: "t"}
	v7 := &Op{Kind: OpRegister, Name: "worker", SessionID: thread, NewToken: "t", V7Semantics: true}

	if got := mk(StatusActive, "bridge-1").ReattachTarget(old); got != nil {
		t.Errorf("a v0.0.6 op matched an ALIAS and recovered %q: that ledger's next op "+
			"names the sibling this op really created, and replay now refuses it", got.ID)
	}
	if got := mk(StatusDormant, thread).ReattachTarget(old); got != nil {
		t.Errorf("a v0.0.6 op recovered a DORMANT row (%q), which it never did", got.ID)
	}
	if got := mk(StatusActive, "bridge-1").ReattachTarget(v7); got == nil || got.ID != "worker" {
		t.Errorf("a v0.0.7 op did not recover by alias: got %v", got)
	}
	if got := mk(StatusDormant, thread).ReattachTarget(v7); got == nil {
		t.Error("a v0.0.7 op did not recover a dormant row")
	}
}

// F2: retention never hides mail it did not evict.
//
// The watermark rose to one past each evicted TERMINAL message. Terminal is not
// oldest: with retention at one, a pending question older than two acknowledged
// notifies sat below the new watermark, present, owed, and invisible to inbox,
// check_in and every hook. Found by the pre-release review with this recipe.
func TestRetentionDoesNotHideAPendingQuestion(t *testing.T) {
	lim := DefaultLimits()
	lim.TerminalRetention = 1
	s := NewState("test", lim)
	t0 := time.Now()
	reg := func(name, tok string) {
		if _, _, err := s.Apply(&Op{
			Kind: OpRegister, Name: name, NewToken: tok,
			AgentKind: KindPersistent, Nonce: "n-" + name, V7Semantics: true,
		}, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	reg("asker", "tok-a")
	reg("busy", "tok-b")
	for _, tok := range []string{"tok-a", "tok-b"} {
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: tok}, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	send := func(kind, body string) uint64 {
		r, _, err := s.Apply(&Op{
			Kind: OpSendMessage, Token: "tok-a", To: "busy",
			MsgType: kind, Body: body, V7Semantics: true,
		}, t0)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r["msg_serial"].(uint64)
	}
	q := send(MsgQuestion, "still waiting on this")
	n1, n2 := send(MsgNotify, "fyi one"), send(MsgNotify, "fyi two")
	for _, serial := range []uint64{n1, n2} {
		if _, _, err := s.Apply(&Op{Kind: OpAckMessage, Token: "tok-b", MsgSerial: serial}, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if m := s.Messages[q]; m == nil || m.State != MsgStatePending {
		t.Fatal("setup: the question is not pending, so there is nothing to hide")
	}

	// SOON, not later. A consumed terminal message older than ConsumedRetention
	// is simply deleted, on a path that never touches the watermark; the first
	// draft of this test swept an hour on and passed against the bug for that
	// reason. The retention path this guards is the one that evicts young
	// terminal mail beyond the per-agent count.
	s.gc(t0.Add(time.Second), false, true)
	if s.Messages[n1] != nil {
		t.Fatal("setup: retention evicted nothing, so the watermark never moved and " +
			"this proves nothing")
	}

	if s.Messages[q] == nil {
		t.Fatal("the pending question was DELETED by retention, which only evicts terminal mail")
	}
	found := false
	for _, m := range s.Inbox("busy") {
		if m.Serial == q {
			found = true
		}
	}
	if !found {
		t.Errorf("the question (serial %d) is still pending and still stored, and the "+
			"inbox no longer shows it: the watermark (%d) passed mail it never evicted",
			q, s.Agents["busy"].TruncatedBefore)
	}
}
