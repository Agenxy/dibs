package core

import (
	"testing"
	"time"
)

// R17-1: a row revived by its nonce yields the session another agent took
// while it was retired.
func TestARevivedRowYieldsASessionTakenWhileItWasGone(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "aaa-old", NewToken: "tok-old", AgentKind: KindPersistent, Nonce: "n-old", SessionAlias: thread, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	s.Agents["aaa-old"].Status = StatusClosed // signed off
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "zzz-current", NewToken: "tok-cur", AgentKind: KindPersistent, Nonce: "n-cur", SessionAlias: thread, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	if !s.Agents["zzz-current"].HoldsSessionForTest(thread) {
		t.Fatal("setup: the current agent does not hold the thread")
	}
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "aaa-old", NewToken: "tok-old2", AgentKind: KindPersistent, Nonce: "n-old", V7Semantics: true}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_id"] != "aaa-old" || s.Agents["aaa-old"].Status != StatusActive {
		t.Fatalf("setup: the nonce did not revive the old row (%v)", res)
	}
	if s.Agents["aaa-old"].HoldsSessionForTest(thread) {
		t.Error("the revived row still holds the thread another agent took while it was signed " +
			"off: two active holders, and hooks resolve to whichever sorts first")
	}
	if !s.Agents["zzz-current"].HoldsSessionForTest(thread) {
		t.Error("the live holder lost the thread to the revived row")
	}
}
