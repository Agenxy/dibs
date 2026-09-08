package core

import (
	"testing"
	"time"
)

// R6-1: a nonce recovery that states a session_id takes it from the old
// holder. The drop honoured only the alias the daemon joins at ingress, so the
// primary id a caller states stayed with both rows: the coin flip on every
// hook that the drop exists to end, on the path every reattaching agent takes.
func TestNonceRecoveryWithASessionIDTakesItFromTheOldHolder(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "old", NewToken: "tok-old", AgentKind: KindPersistent, Nonce: "no", SessionID: thread, V7Semantics: true},
		{Kind: OpRegister, Name: "new", NewToken: "tok-new", AgentKind: KindPersistent, Nonce: "nn", V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	s.Agents["old"].Status = StatusDormant
	s.Agents["new"].Status = StatusDormant
	// The ingress saw "old" not answering and wrote its name down; the fold
	// reads that and never re-decides.
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "new", Nonce: "nn", NewToken: "tok-new2",
		SessionID: thread, SessionTakenFrom: "old", V7Semantics: true,
	}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_id"] != "new" || !s.Agents["new"].HoldsSessionForTest(thread) {
		t.Fatalf("setup: the nonce recovery did not land on \"new\" holding the thread (%v), "+
			"so the assertion below proves nothing", res)
	}
	if s.Agents["old"].HoldsSessionForTest(thread) {
		t.Error("the old holder still holds the session after a nonce recovery stated it: " +
			"two stated holders, and its next check_in regains hook routing for the thread")
	}
}
