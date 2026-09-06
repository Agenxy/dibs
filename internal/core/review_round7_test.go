package core

import (
	"testing"
	"time"
)

// R7-2: one registration can take two ids from two rows, and the ingress
// record names one of them. Every other holder loses both.
func TestATakenSessionIsDroppedFromEveryHolderNotJustTheNamedOne(t *testing.T) {
	const primary = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	const alias = "01a0a0a0-0eaf-7f60-81cc-6ab1298d76ff"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "a", NewToken: "tok-a", AgentKind: KindPersistent, Nonce: "na", SessionID: primary, V7Semantics: true},
		{Kind: OpRegister, Name: "b", NewToken: "tok-b", AgentKind: KindPersistent, Nonce: "nb", SessionAlias: alias, V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if !s.Agents["b"].HoldsSessionForTest(alias) {
		t.Fatal("setup: b does not hold the alias, so nothing below proves anything")
	}
	s.Agents["a"].Status, s.Agents["b"].Status = StatusDormant, StatusDormant
	// The primary admission wrote last and named a; the alias came from b.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "c", NewToken: "tok-c", AgentKind: KindPersistent, Nonce: "nc",
		SessionID: primary, SessionAlias: alias, SessionTakenFrom: "a", V7Semantics: true,
	}, t0); err != nil {
		t.Fatal(err)
	}
	c := s.Agents["c"]
	if !c.HoldsSessionForTest(primary) || !c.HoldsSessionForTest(alias) {
		t.Fatal("setup: c did not take both ids, so nothing below proves anything")
	}
	if s.Agents["a"].HoldsSessionForTest(primary) {
		t.Error("a, the row the record named, still holds the primary id")
	}
	if s.Agents["b"].HoldsSessionForTest(alias) {
		t.Error("b, the row the record did not name, still holds the alias c took: two " +
			"stated holders the moment b checks in, and a coin flip on every hook")
	}
}

// R7-3: an explicit bind_session is stated, not guessed.
func TestAnExplicitBindUpgradesAGuessedSession(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "a", NewToken: "tok-a", AgentKind: KindPersistent, Nonce: "na",
		SessionAlias: thread, SessionGuessed: true, V7Semantics: true,
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	if !s.Agents["a"].GuessedSession(thread) {
		t.Fatal("setup: the id was not recorded as a guess, so the upgrade below proves nothing")
	}
	if _, _, err := s.Apply(&Op{Kind: OpBindSession, Token: "tok-a", SessionID: thread, V7Semantics: true}, t0); err != nil {
		t.Fatal(err)
	}
	if s.Agents["a"].GuessedSession(thread) {
		t.Error("after an explicit bind_session the id is still a guess: any live claim " +
			"may take it from this active agent, and the confirmation established nothing")
	}
}
