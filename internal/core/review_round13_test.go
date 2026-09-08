package core

import (
	"testing"
	"time"
)

// R13-1: a same-nonce register inside the TTL that states a session_id and
// no alias takes that session, and it is the one to wake.
func TestALiveResumeTakesAStatedSessionID(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, Nonce: "n", SessionAlias: a, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok2", AgentKind: KindPersistent, Nonce: "n", SessionID: b, V7Semantics: true}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res["resumed"] != true {
		t.Fatalf("setup: the register inside the TTL did not resume (%v)", res)
	}
	l := s.Agents["r"]
	if !l.HoldsSessionForTest(b) || l.CurrentSession != b {
		t.Errorf("after resuming with session_id %s the agent holds it: %v, current %q. The "+
			"resume reported success and kept thread %s, so the wake resumes the one it left",
			b, l.HoldsSessionForTest(b), l.CurrentSession, a)
	}
}
