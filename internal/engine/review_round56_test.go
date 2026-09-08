package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The ingress inferred a session by directory whenever the op itself
// carried no session fields, without asking whether the row already held a
// stated thread: an agent that registered stating thread A, in a directory
// another session had announced from, was bound to that session on its
// next check_in, the wake resumed the wrong thread, and the other session's
// hooks resolved to A's mailbox.
func TestAPlainCheckInDoesNotGuessOverAStatedThread(t *testing.T) {
	const (
		dir     = "/work/shared"
		threadA = "01a00042-2222-7f60-81cc-6ab1298d76ec"
		threadB = "19d67315-7718-491e-be3f-3864f577eeed"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-r-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: threadA, Agent: &core.AgentInfo{Harness: "Codex", CWD: dir}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	// Another session announces from the same directory, unclaimed.
	if _, err := e.HookPoll(ctx, threadB, "SessionStart", dir, false, false); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal("setup:", err)
	}
	l := st.Agents["r"]
	if l.HoldsSessionForTest(threadB) || l.CurrentSession != threadA {
		t.Fatalf("a plain check_in bound the directory's guess %s over the stated thread %s (current %q)",
			threadB, threadA, l.CurrentSession)
	}
	if got := threadIDOf(l); got != threadA {
		t.Fatalf("the wake resumes %q, not the stated thread", got)
	}
	if got := st.AgentForHook(threadB, dir); got != nil && got.ID == "r" {
		t.Fatalf("the other session's hooks resolve to %s's mailbox", got.ID)
	}
}

// The ordering test for a supplied alias tested the announcement and the
// claim rule separately; this submits the call through ingress and checks
// which id the row holds.
func TestASuppliedAliasBeatsTheDirectoryGuessThroughIngress(t *testing.T) {
	const (
		dir          = "/work/shared"
		somebodyElse = "19d67315-7718-491e-be3f-3864f577eeed"
		mine         = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.HookPoll(ctx, somebodyElse, "SessionStart", dir, false, false); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "me", Nonce: "n-me-0123456789abcde", AgentKind: core.KindPersistent, SessionAlias: mine, Agent: &core.AgentInfo{CWD: dir}}); err != nil {
		t.Fatal("setup:", err)
	}
	l := st.Agents["me"]
	if !l.HoldsSessionForTest(mine) {
		t.Fatalf("the alias that arrived with the call is not bound: the row holds %v", sessionsOf(l))
	}
	if l.HoldsSessionForTest(somebodyElse) {
		t.Fatalf("the directory's guess %s was bound beside a supplied alias: another session's hooks "+
			"resolve here", somebodyElse)
	}
}
