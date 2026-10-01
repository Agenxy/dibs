package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// continuationBoard is a running engine with a Codex-shaped agent whose wake
// command is a stand-in that succeeds, so a wake is delivered through the
// production path and nothing real starts.
type continuationBoard struct {
	e     *Engine
	ctx   context.Context
	token string
}

const contThread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"

func newContinuationBoard(t *testing.T) *continuationBoard {
	t.Helper()
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("no /usr/bin/true on this platform")
	}
	t.Setenv("CODEX_HOME", t.TempDir()) // never the operator's threads
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/true", "{thread}"}}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", Nonce: "n-worker-0123456789abcdef",
		AgentKind: core.KindPersistent, SessionID: contThread,
		Agent: &core.AgentInfo{Harness: "Codex", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	b := &continuationBoard{e: e, ctx: ctx, token: res["token"].(string)}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: b.token}); err != nil {
		t.Fatalf("setup: check_in: %v", err)
	}
	return b
}

func (b *continuationBoard) declare(t *testing.T, text, waiting string) {
	t.Helper()
	if _, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpSetSlot, Token: b.token, SlotID: "s1", Text: text, Waiting: waiting,
	}); err != nil {
		t.Fatalf("setup: declare: %v", err)
	}
}

// wake delivers a wake through the path every wake takes.
func (b *continuationBoard) wake(t *testing.T) {
	t.Helper()
	var l *core.Agent
	if _, err := b.e.query(b.ctx, func() core.Result { l = b.e.state.Agents["worker"]; return nil }); err != nil {
		t.Fatal(err)
	}
	plan, ok := b.e.wakeFor(l, core.MsgQuestion, questionFor("worker"))
	if !ok || len(plan.argv) == 0 {
		t.Fatal("setup: no wake planned, so nothing below tests a turn Dibs started")
	}
	// As every production wake does: run it, then record that it exited, or
	// the board believes it is still running and refuses the next one.
	ok = b.e.runWakeAndReport(plan, "worker")
	b.e.wakeExited("worker", plan.thread)
	if !ok {
		t.Fatal("setup: the stand-in wake command failed")
	}
}

func (b *continuationBoard) stop(t *testing.T, stopActive bool) core.Result {
	t.Helper()
	got, err := b.e.HookPoll(b.ctx, contThread, "Stop", "", stopActive, true) // strict: Codex
	if err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	return got
}

func continued(r core.Result) bool { return r["decision"] == "block" }

// The stall, end to end: a turn Dibs started ends while the agent holds a
// declaration that says it is working. The Stop hook continues the turn and
// hands back the declaration in the agent's own words, in the shape Codex
// accepts at Stop. Measured case: codex-k7-0 declared "Implementing ... C",
// answered the question it was woken for, and completed, for hours.
func TestATurnDibsStartedIsContinuedWhileItsDeclarationIsOpen(t *testing.T) {
	b := newContinuationBoard(t)
	const work = "Implementing approved VZ durable resume as stacked C"
	b.declare(t, work, "")
	b.wake(t)

	got := b.stop(t, false)
	if !continued(got) {
		t.Fatalf("the turn ended with declared work open and was not continued: %v", keysOf(got))
	}
	reason, _ := got["reason"].(string)
	if !strings.Contains(reason, work) {
		t.Errorf("the reason does not quote the declaration, so the model resumes "+
			"\"something\" rather than its task: %q", reason)
	}
	if _, bad := got["hookSpecificOutput"]; bad {
		t.Error("a Codex Stop reply carries hookSpecificOutput, which fails its parse")
	}
	// A continued Stop is not a turn that ended: the later wakes count from a
	// real end, and one that was only continued has not happened.
	if _, err := b.e.query(b.ctx, func() core.Result {
		if _, ended := b.e.turnEnded["worker"]; ended {
			t.Error("a continued Stop was recorded as the end of the turn")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Bounded: twice for one version of the declaration, then left alone.
	if !continued(b.stop(t, false)) {
		t.Error("the second continuation was refused; the budget is two")
	}
	if continued(b.stop(t, false)) {
		t.Fatal("a third continuation with no progress: an agent that keeps stopping " +
			"would be driven in a loop on its operator's allowance")
	}
	// Progress the daemon can see resets it: the declaration changed.
	b.declare(t, work+", opened pr:1700", "")
	if !continued(b.stop(t, false)) {
		t.Error("a changed declaration did not reset the budget")
	}
}

// And everything that must not be continued is not.
func TestOnlyADibsStartedTurnWithWorkInProgressIsContinued(t *testing.T) {
	t.Run("no Dibs wake: a person started this turn", func(t *testing.T) {
		b := newContinuationBoard(t)
		b.declare(t, "some work", "")
		if continued(b.stop(t, false)) {
			t.Error("continued a turn Dibs did not start")
		}
	})
	t.Run("a person prompted after the wake", func(t *testing.T) {
		b := newContinuationBoard(t)
		b.declare(t, "some work", "")
		b.wake(t)
		if _, err := b.e.HookPoll(b.ctx, contThread, "UserPromptSubmit", "", false, false); err != nil {
			t.Fatal(err)
		}
		if continued(b.stop(t, false)) {
			t.Error("continued a turn after a person prompted: the turn is ending to hand back to them")
		}
	})
	t.Run("the declaration is waiting", func(t *testing.T) {
		b := newContinuationBoard(t)
		b.declare(t, "PR #1700 merge", "ci")
		b.wake(t)
		if continued(b.stop(t, false)) {
			t.Error("continued an agent whose declaration says it is blocked")
		}
	})
	t.Run("nothing declared", func(t *testing.T) {
		b := newContinuationBoard(t)
		b.wake(t)
		if continued(b.stop(t, false)) {
			t.Error("continued an agent with no declared work")
		}
	})
	t.Run("a Stop hook already continued this turn", func(t *testing.T) {
		b := newContinuationBoard(t)
		b.declare(t, "some work", "")
		b.wake(t)
		if continued(b.stop(t, true)) {
			t.Error("continued a turn Codex reports a Stop hook already continued")
		}
	})
}

// A long turn after a continuation is progress: the budget resets.
func TestALongTurnResetsTheContinuationBudget(t *testing.T) {
	open := []core.Slot{{ID: "s1", Text: "work", UpdatedSerial: 7}}
	t0 := time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC)
	rec := continuation{}
	var ok bool
	for range maxContinuations {
		_, rec, ok = decideContinuation(open, true, rec, t0)
		if !ok {
			t.Fatal("setup: the budget ran out early")
		}
	}
	if _, _, ok = decideContinuation(open, true, rec, t0.Add(time.Minute)); ok {
		t.Fatal("setup: a short turn after the budget was continued")
	}
	if _, _, ok = decideContinuation(open, true, rec, t0.Add(progressTurn)); !ok {
		t.Error("a turn that ran the full progress window did not reset the budget")
	}
}
