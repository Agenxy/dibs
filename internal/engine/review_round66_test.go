package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A wake command runs the agent's whole turn, up to two hours, so it can
// outlive the thread it woke: a wake started on thread A, the agent moved to
// thread B and called in, then A exited. Stamping the turn end against the
// agent marked B finished, and the recency guard then let blocking mail
// launch a second activation on a thread that is running.
func TestAnOldWakesExitDoesNotMarkTheCurrentThreadFinished(t *testing.T) {
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	e, st := wakeEngine(t, WakeCommand{Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second})
	l := bridgeAgent("mover", "Codex", threadA)
	l.SessionAliases = []string{threadA, threadB}
	l.CurrentSession = threadB // it has moved to B
	st.Agents["mover"] = l

	// B is running: it called in just now.
	e.seen["mover"] = time.Now()
	// The wake that had started on the OLD thread A now exits.
	e.wakeExitedDecision("mover", threadA)
	if !e.recentlyInTouch(l) {
		t.Error("an old wake's exit on thread A marked the agent finished, so its current " +
			"thread B reads as idle despite calling in just now: the next blocking message " +
			"launches a second activation on a thread that is running")
	}

	// A wake exiting on the CURRENT thread still ends the turn, so the fix does
	// not blind the guard.
	e.wakeExitedDecision("mover", threadB)
	if e.recentlyInTouch(l) {
		t.Error("a wake exiting on the current thread did not end the turn")
	}
}

// A hook from a thread the agent has LEFT must neither be handed the digest
// nor spend a pending notify's one-shot wake. After a move from A to B, a
// late Stop from A resolves to the same row through a retained alias.
func TestAStaleThreadsHookDoesNotConsumeTheCurrentThreadsWake(t *testing.T) {
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	asker, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "a-1", Nonce: "n-a"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "mover", NewToken: "m-1", Nonce: "n-m",
		SessionID: "host-1", SessionAlias: threadA, Agent: &core.AgentInfo{CWD: "/work"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	// It moves to thread B: same nonce, a new thread alias.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "mover", NewToken: "m-2", Nonce: "n-m",
		SessionID: "host-1", SessionAlias: threadB, Agent: &core.AgentInfo{CWD: "/work"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	if got := st.Agents["mover"].CurrentSession; got != threadB {
		t.Fatalf("setup: the agent's current session is %q, not thread B; the move did not take", got)
	}
	// A pending notify is waiting: it wakes once.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: asker["token"].(string),
		To: "mover", MsgType: core.MsgNotify, Body: "one-shot",
	}); err != nil {
		t.Fatal("setup:", err)
	}

	// A late Stop from the OLD thread A must not be handed the digest.
	got, err := e.HookPoll(ctx, threadA, "Stop", "/work", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["hookSpecificOutput"]; ok {
		t.Fatal("a Stop from the thread the agent LEFT was handed the digest and spent the " +
			"notify's one wake: the current thread will never hear of it")
	}

	// The current thread B still receives it.
	got, err = e.HookPoll(ctx, threadB, "Stop", "/work", false, false)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("the current thread B was handed no digest for its pending notify: %v", got)
	}
	if ctxText, _ := out["additionalContext"].(string); ctxText == "" {
		t.Fatalf("the current thread B got an empty digest: %v", got)
	}
}
