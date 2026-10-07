// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
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
	stopWakeTimersOnCleanup(t, e)
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
	// Refusals are Debug-level. Preserve the actual production decision's
	// evidence when this setup times out, rather than guessing why it refused.
	var wakeLog continuationWakeLog
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&wakeLog, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	// A real wake needs actual outstanding mail. A synthetic event with an
	// empty inbox now correctly settles without starting a turn (#7810).
	sender, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpRegister, Name: "wake-sender",
		Nonce: "wake-fixture-0123456789abcdef", AgentKind: core.KindPersistent,
	})
	if err != nil {
		t.Fatal("wake sender setup:", err)
	}
	mail, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender["token"].(string),
		To: "worker", MsgType: core.MsgHandoff, Body: "wake fixture",
	})
	if err != nil {
		t.Fatal("wake mail setup:", err)
	}
	var l *core.Agent
	if _, err := b.e.query(b.ctx, func() core.Result { l = b.e.state.Agents["worker"]; return nil }); err != nil {
		t.Fatal(err)
	}
	// A send in the test may already have started a real wake through the
	// event path, and wakeFor rightly refuses a second while it runs. On a
	// slow machine it was still running here, which failed this setup on CI
	// and nowhere else. Wait for it, as the product would.
	var plan wakePlan
	ok := false
	for range 300 {
		if plan, ok = b.e.wakeFor(l, core.MsgQuestion, questionFor("worker")); ok {
			break
		}
		<-time.After(10 * time.Millisecond)
	}
	if !ok || len(plan.argv) == 0 {
		t.Fatalf("setup: no wake planned, so nothing below tests a turn Dibs started\nactual wake log:\n%s", wakeLog.String())
	}
	// As every production wake does: run it, then record that it exited, or
	// the board believes it is still running and refuses the next one.
	ok = b.e.runWakeAndReport(plan, "worker")
	b.e.wakeExited("worker", plan.thread)
	if !ok {
		t.Fatal("setup: the stand-in wake command failed")
	}
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpAckMessage, Token: b.token, MsgSerial: mail["msg_serial"].(uint64)}); err != nil {
		t.Fatal("wake mail acknowledgement:", err)
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

// A declaration that keeps changing is progress by the per-version rule, and
// that let a loop through: measured 2026-10-01, a worker rewrote its
// declaration's text on every Stop and was continued four times in three
// minutes. At most maxInWindow continuations in continuationWindow, whatever
// the declaration does.
func TestRedeclaringOnEveryStopIsNotALicenceToLoop(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 8, 9, 0, 0, time.UTC)
	rec := continuation{}
	allowed := 0
	for i := range 6 {
		open := []core.Slot{{ID: "s1", Text: fmt.Sprintf("waiting on CI, refresh %d", i), UpdatedSerial: uint64(100 + i)}}
		var ok bool
		_, rec, ok = decideContinuation(open, true, rec, t0.Add(time.Duration(i)*30*time.Second))
		if ok {
			allowed++
		}
	}
	if allowed != maxInWindow {
		t.Errorf("continued %d times in three minutes of re-declaring, want %d", allowed, maxInWindow)
	}
	open := []core.Slot{{ID: "s1", Text: "new work", UpdatedSerial: 200}}
	if _, _, ok := decideContinuation(open, true, rec, t0.Add(continuationWindow+time.Minute)); !ok {
		t.Error("the window never reopened")
	}
}

// stopWakeTimersOnCleanup stops every deferred wake the engine armed, when the
// test ends. A deferred wake is a time.AfterFunc that reads package state
// (peerAlive) when it fires, and one armed by a test that sent mail fired
// seconds later inside a DIFFERENT test that was rewriting that variable: a
// data race the race detector pinned on the innocent test.
func stopWakeTimersOnCleanup(t *testing.T, e *Engine) {
	t.Helper()
	t.Cleanup(func() {
		e.wakers.mu.Lock()
		defer e.wakers.mu.Unlock()
		for _, tm := range e.wakers.deferred {
			tm.Stop()
		}
		e.wakers.deferred = nil
	})
}

// The writer loop and wake runner can log concurrently with the failure read.
// Keep a bounded tail so repeated cooldown refusals do not bury the verdict.
type continuationWakeLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *continuationWakeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.buf.Write(p)
	if l.buf.Len() > 8192 {
		l.buf.Next(l.buf.Len() - 8192)
	}
	return n, err
}

func (l *continuationWakeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
