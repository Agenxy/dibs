package core

import (
	"testing"
	"time"
)

// R14-1: a same-nonce register that states a session the row held as a guess
// confirms it, and a confirmed session is not a guess any more.
func TestARegisterConfirmingAnInferredSessionMakesItStated(t *testing.T) {
	const a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, Nonce: "n", SessionAlias: a, SessionGuessed: true, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	if !s.Agents["r"].GuessedSession(a) {
		t.Fatal("setup: the session was not recorded as a guess, so the confirmation below proves nothing")
	}
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok2", AgentKind: KindPersistent, Nonce: "n", SessionID: a, V7Semantics: true}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res["resumed"] != true {
		t.Fatalf("setup: the register did not resume (%v)", res)
	}
	if s.Agents["r"].GuessedSession(a) {
		t.Error("after register(name, nonce, session_id) confirmed the inferred session it is still a " +
			"guess: another agent's metadata can take this active session, hooks and wakes with it")
	}
}
