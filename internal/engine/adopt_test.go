package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// An agent with no session is unreachable by every lifecycle hook, forever.
//
// The wake path resolves an agent by the session id its harness quotes, and
// AgentForHook deliberately refuses the cwd fallback when a session id was
// supplied and matched nothing: without that refusal, any unregistered session
// in a shared directory was handed another agent's private mail. Correct, and
// it means an agent carrying no session simply cannot be woken, however well
// the plugin is installed.
//
// Measured on a live board: nine consecutive wake polls resolved to nobody
// while that agent's mail sat unread for days, and `dibs doctor` reported
// "harness hooks resolving", because for every other agent they were.
//
// So the first authenticated call the agent makes through the bridge repairs
// it. This goes through the ENGINE the way that call does, and asserts the hook
// path before and after: a test of AdoptSession alone would pass while the wake
// path stayed dead, which is the whole failure being fixed.
func TestAnAgentWithNoSessionIsAttachedToTheOneItRunsIn(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "orphan", Nonce: "n-orphan"})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)
	if tok == "" {
		t.Fatal("setup: no token")
	}
	const sid = "harness-session-1"

	// Before: the hook quotes a session this board has never heard of.
	if got, err := e.HookPoll(ctx, sid, "Stop", "/elsewhere", false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	} else if got["agent"] != nil {
		t.Fatalf("hook_poll resolved %v before anything bound the session", got["agent"])
	}

	adopted, err := e.AdoptSession(ctx, tok, sid)
	if err != nil {
		t.Fatalf("AdoptSession: %v", err)
	}
	if !adopted {
		t.Fatal("an agent with no session was not adopted, so nothing will ever wake it")
	}

	// After: the same hook call, unchanged, now names the agent.
	got, err := e.HookPoll(ctx, sid, "Stop", "/elsewhere", false, false)
	if err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	if got["agent"] != "orphan" {
		t.Fatalf("hook_poll = %v, want the agent: the wake path is still dead", got)
	}

	// Repair, never redirection. An agent that already has a session was bound
	// by the path that knows best, its own registration; letting an ambient
	// header overwrite that would let one bridge steal another agent's wake
	// path, which is worse than the problem being solved.
	again, err := e.AdoptSession(ctx, tok, "somebody-elses-session")
	if err != nil {
		t.Fatalf("AdoptSession: %v", err)
	}
	if again {
		t.Error("an agent's existing session was overwritten from an ambient header")
	}
	if got, _ := e.HookPoll(ctx, sid, "Stop", "/elsewhere", false, false); got["agent"] != "orphan" {
		t.Error("the original binding was lost")
	}
}

// A wake path that fires and resolves nobody must not read as healthy.
//
// pollUnresolved was counted from the day this file was written and never once
// reached a verdict: the check asked "did ANY call resolve", which a machine
// running several agents always answers yes to. So one agent's wake path being
// completely dead read `ok`, and the daemon reported "harness hooks resolving"
// while that agent's mail went undelivered. A count nothing reads is not a
// diagnostic.
func TestWakeCallsThatResolveNobodyAreNotReportedAsHealthy(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})

	// The shape that used to read "ok": plenty resolving, some not.
	e.noteHook("guard", true)
	e.noteHook("poll", true)
	e.noteHook("poll", true)
	e.noteHook("poll", false)

	h := e.HookHealth()
	if h.Verdict == "ok" {
		t.Fatalf("verdict = %q with %d unresolved wake call(s): a partial failure that "+
			"reads as success is worse than one that reads as nothing",
			h.Verdict, h.PollUnresolved)
	}
	if h.Hint == "" {
		t.Error("no hint: every diagnosis names the corrective action")
	}

	// And a board where everything resolves is still healthy, or the check is
	// noise and gets ignored on the day it matters.
	clean := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	clean.noteHook("poll", true)
	clean.noteHook("guard", true)
	if v := clean.HookHealth().Verdict; v != "ok" {
		t.Errorf("verdict = %q on a board where every call resolved", v)
	}
}

// Only one ambient caller adopts an unbound session.
//
// The check and the bind are separate trips through the writer loop, so
// concurrent callers all saw an empty SessionID and all bound: each was told it
// had adopted the agent, and the id that stuck was whichever finished last.
// Hook delivery then reaches one session while every other holder believes it
// owns the mailbox, which is the failure this repair exists to prevent, caused
// by the repair.
//
// Twenty callers, released together, because one at a time is exactly the case
// that already worked.
func TestOnlyOneAmbientCallerAdoptsASession(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "orphan", Nonce: "n-orphan"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)

	const callers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var adopted []string
	for i := 0; i < callers; i++ {
		sid := fmt.Sprintf("ambient-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, err := e.AdoptSession(ctx, tok, sid); err == nil && ok {
				mu.Lock()
				adopted = append(adopted, sid)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(adopted) != 1 {
		t.Errorf("%d of %d concurrent callers were told they adopted the session: %v\n"+
			"Only one can own it. The rest go on believing they will receive this "+
			"agent's mail, and its hooks deliver to whichever bind landed last",
			len(adopted), callers, adopted)
	}
	// And the winner is the one the board actually holds.
	if len(adopted) == 1 {
		got, err := e.query(ctx, func() core.Result {
			return core.Result{"sid": st.Agents["orphan"].SessionID}
		})
		if err != nil {
			t.Fatal(err)
		}
		if got["sid"] != adopted[0] {
			t.Errorf("the caller told it adopted the session is %q and the board holds "+
				"%v", adopted[0], got["sid"])
		}
	}
}
