package core

import (
	"testing"
	"time"
)

// R16-1: nonce recovery that states the session the row held as a guess
// confirms it, as the live resume does.
func TestNonceRecoveryConfirmsAnInferredSession(t *testing.T) {
	const a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, Nonce: "n", SessionAlias: a, SessionGuessed: true, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	if !s.Agents["r"].GuessedSession(a) {
		t.Fatal("setup: the session was not recorded as a guess")
	}
	s.Agents["r"].Status = StatusDormant
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok2", AgentKind: KindPersistent, Nonce: "n", SessionID: a, V7Semantics: true}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res["reattached"] != true {
		t.Fatalf("setup: the register did not take the nonce-recovery path (%v)", res)
	}
	if s.Agents["r"].GuessedSession(a) {
		t.Error("after nonce recovery stated the inferred session it is still a guess: a " +
			"stranger's metadata can take it, and hooks and wakes resolve to the stranger")
	}
}
