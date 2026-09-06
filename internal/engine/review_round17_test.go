package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R17-2: a register that states its thread is not given the directory's
// guess as an alias, so the wake resumes the stated thread.
func TestAStatedThreadIsNotOverriddenByADirectoryGuess(t *testing.T) {
	const (
		dir       = "/work/guessed"
		announced = "19d67315-7718-491e-be3f-3864f577eeed"
		stated    = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.HookPoll(ctx, announced, "SessionStart", dir, false, false); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "explicit", Nonce: "n-explicit-0123456789", AgentKind: core.KindPersistent, SessionID: stated, Agent: &core.AgentInfo{Harness: "Codex", CWD: dir}}); err != nil {
		t.Fatal(err)
	}
	l := st.Agents["explicit"]
	if got := threadIDOf(l); got != stated {
		t.Errorf("registered naming thread %s, the wake resumes %s: the directory's guess "+
			"overrode the stated thread", stated, got)
	}
}

// R17-3: an agent registering with the session id it states takes it from
// a holder that only guessed it, as a claim by alias would.
func TestAnExplicitSessionIDTakesAGuessedBinding(t *testing.T) {
	const (
		dir       = "/work/inferred"
		announced = "19d67315-7718-491e-be3f-3864f577eeed"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.HookPoll(ctx, announced, "SessionStart", dir, false, false); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "guesser", Nonce: "n-guesser-0123456789", AgentKind: core.KindPersistent, Agent: &core.AgentInfo{CWD: dir}}); err != nil {
		t.Fatal("setup:", err)
	}
	if g := st.Agents["guesser"]; !g.HoldsSessionForTest(announced) || !g.GuessedSession(announced) {
		t.Fatal("setup: the inference did not bind the announced session as a guess")
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "rightful", Nonce: "n-rightful-0123456789", AgentKind: core.KindPersistent, SessionID: announced, SessionAlias: announced, Agent: &core.AgentInfo{CWD: t.TempDir()}}); err != nil {
		t.Fatalf("the rightful agent stating its own session id was refused: %v", err)
	}
	if !st.Agents["rightful"].HoldsSessionForTest(announced) || st.Agents["guesser"].HoldsSessionForTest(announced) {
		t.Error("the stated session did not move from the agent that guessed it to the one that stated it")
	}
}
