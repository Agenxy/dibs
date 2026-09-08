package core

import (
	"testing"
	"time"
)

// R22-1: a register vetted for a dormant peer's thread does not strip an
// active peer's synthetic session id that nobody vetted.
func TestAnActivePeersUnvettedBindingSurvivesAnotherTake(t *testing.T) {
	const (
		synthetic = "host-777"
		thread    = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "active-peer", NewToken: "tok-a", AgentKind: KindPersistent, Nonce: "na", SessionID: synthetic, V7Semantics: true},
		{Kind: OpRegister, Name: "dormant-peer", NewToken: "tok-d", AgentKind: KindPersistent, Nonce: "nd", SessionAlias: thread, V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	s.Agents["dormant-peer"].Status = StatusDormant
	// The ingress vetted the thread (dormant holder: claimable) and named it;
	// the synthetic id is not thread-shaped and was never vetted.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "newcomer", NewToken: "tok-n", AgentKind: KindPersistent, Nonce: "nn",
		SessionID: synthetic, SessionAlias: thread, SessionTakenFrom: "dormant-peer", V7Semantics: true,
	}, t0); err != nil {
		t.Fatal(err)
	}
	if !s.Agents["active-peer"].HoldsSessionForTest(synthetic) {
		t.Error("the active peer lost its stated session id to a register that was vetted for " +
			"somebody else's thread: its hooks now resolve to the newcomer")
	}
	if s.Agents["dormant-peer"].HoldsSessionForTest(thread) {
		t.Error("the dormant peer the ingress named still holds the thread")
	}
}
