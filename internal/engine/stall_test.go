// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

var stallT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// The decision, step by step: a Dibs-started agent whose turns keep ending
// with its declaration unchanged is woken at 10, 30 and 60 minutes after each
// turn end, then reported stalled, and a changed declaration starts over.
func TestAStoppedAgentIsWokenOnABackoffAndThenReportedStalled(t *testing.T) {
	work := []core.Slot{{ID: "s1", Text: "build C", UpdatedSerial: 40}}
	in := workInput{slots: work, started: true, ended: stallT0, idle: true}
	rec := workRecord{}
	step := func(now time.Time, want workAction) {
		t.Helper()
		var got workAction
		got, rec = decideWork(in, rec, now)
		if got != want {
			t.Fatalf("at +%v: action %d, want %d (record %+v)", now.Sub(stallT0), got, want, rec)
		}
	}
	step(stallT0.Add(5*time.Minute), workNothing)
	step(stallT0.Add(10*time.Minute), workContinue)
	in.ended = stallT0.Add(12 * time.Minute) // that wake ran a turn, which ended
	step(stallT0.Add(30*time.Minute), workNothing)
	step(stallT0.Add(42*time.Minute), workContinue)
	in.ended = stallT0.Add(43 * time.Minute)
	step(stallT0.Add(103*time.Minute), workContinue)
	in.ended = stallT0.Add(104 * time.Minute)
	step(stallT0.Add(105*time.Minute), workStall)
	step(stallT0.Add(300*time.Minute), workNothing) // stalled stays stalled, and quiet

	in.slots = []core.Slot{{ID: "s1", Text: "build C, pr:1700 open", UpdatedSerial: 52}}
	in.ended = stallT0.Add(301 * time.Minute)
	step(stallT0.Add(302*time.Minute), workNothing)
	if rec.wakes != 0 || !rec.stalledAt.IsZero() {
		t.Errorf("a changed declaration did not start over: %+v", rec)
	}
}

func TestOnlyADibsStartedStoppedAgentIsWokenForItsWork(t *testing.T) {
	work := []core.Slot{{ID: "s1", Text: "build C", UpdatedSerial: 40}}
	late := stallT0.Add(3 * time.Hour)
	for name, in := range map[string]workInput{
		"a person's session":   {slots: work, started: false, ended: stallT0, idle: true},
		"a turn still running": {slots: work, started: true, idle: false},
		"nothing declared":     {started: true, ended: stallT0, idle: true},
		"waiting, no recheck":  {slots: []core.Slot{{ID: "s1", Text: "x", Waiting: "k7-dev", UpdatedSerial: 1}}, started: true, ended: stallT0, idle: true},
	} {
		if got, _ := decideWork(in, workRecord{}, late); got != workNothing {
			t.Errorf("%s: action %d, want nothing", name, got)
		}
	}
}

// A declared wait with a recheck time is woken at that time, three times, and
// then reported stalled: it asked to be told, so it is, but not forever.
func TestADeclaredRecheckIsHonouredAndBounded(t *testing.T) {
	in := workInput{
		slots: []core.Slot{{ID: "s1", Text: "PR #1700 CI", Waiting: "ci", RecheckSec: 1200, UpdatedSerial: 9}},
		idle:  true,
	}
	_, rec := decideWork(in, workRecord{}, stallT0) // first seen
	for i, want := range []workAction{workRecheck, workRecheck, workRecheck, workStall} {
		var got workAction
		got, rec = decideWork(in, rec, stallT0.Add(time.Duration(20*(i+1))*time.Minute))
		if got != want {
			t.Fatalf("recheck %d: action %d, want %d", i+1, got, want)
		}
	}
	busy := in
	busy.idle = false
	if got, _ := decideWork(busy, workRecord{version: 9, versionAt: stallT0}, stallT0.Add(time.Hour)); got != workNothing {
		t.Error("woke an agent for a recheck while its turn was running")
	}
}

// The whole chain on a running engine: an assigned request, a real wake, the
// agent's own Stop hook, the backoff wakes, the `stalled` row and the notice to
// the agent that assigned the work.
func TestAStalledWorkerIsShownAndItsAssignerIsTold(t *testing.T) {
	if _, err := os.Stat("/usr/bin/touch"); err != nil {
		t.Skip("no /usr/bin/touch on this platform")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	cwd := t.TempDir()
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {
		Argv: []string{"/usr/bin/touch", "{thread}"}, Cooldown: time.Millisecond,
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWakeTimersOnCleanup(t, e)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		res, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup: %s: %v", op.Kind, err)
		}
		return res
	}
	worker := do(&core.Op{
		Kind: core.OpRegister, Name: "worker", Nonce: "n-worker-0123456789abcdef",
		AgentKind: core.KindPersistent, SessionID: contThread,
		Agent: &core.AgentInfo{Harness: "Codex", CWD: cwd},
	})["token"].(string)
	lead := do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "n-lead-0123456789abcdef"})["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: worker})
	do(&core.Op{Kind: core.OpAckBoard, Token: lead})
	sent := do(&core.Op{Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgRequest, Body: "build C", OpID: "a1"})
	serial, _ := sent["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: serial, Disposition: "approve"})
	do(&core.Op{Kind: core.OpSetSlot, Token: worker, SlotID: "s1", Text: "build C"})

	b := &continuationBoard{e: e, ctx: ctx, token: worker}
	b.wake(t) // a Dibs wake starts the turn
	for range maxContinuations {
		if !continued(b.stop(t, false)) {
			t.Fatal("setup: the in-turn continuations did not run")
		}
	}
	if continued(b.stop(t, false)) {
		t.Fatal("setup: a third in-turn continuation")
	}

	// THE REAL LOOP drives it from here: no tick is called by hand, so this
	// fails if Run stops calling stallTick. The backoff is shortened, the
	// order and count are production's.
	prevBackoff, prevEvery := continuationBackoff, stallEvery
	continuationBackoff = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond}
	stallEvery = 0
	t.Cleanup(func() { continuationBackoff, stallEvery = prevBackoff, prevEvery })

	touched := filepath.Join(cwd, contThread)
	_ = os.Remove(touched)
	waitWoken := func(step string) {
		t.Helper()
		for range 500 {
			if _, err := os.Stat(touched); err == nil {
				_ = os.Remove(touched)
				return
			}
			<-time.After(10 * time.Millisecond)
		}
		t.Fatalf("%s: no wake ran", step)
	}
	for i := range continuationBackoff {
		waitWoken("backoff wake " + string(rune('1'+i)))
		// The woken turn ends at once, as the stalled worker's did.
		if _, err := e.HookPoll(ctx, contThread, "Stop", "", true, true); err != nil {
			t.Fatal(err)
		}
	}
	stalled := false
	for range 500 {
		board, err := e.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range board["agents"].([]map[string]any) {
			if a["id"] == "worker" && a["work"] == "stalled" {
				stalled = true
			}
		}
		if stalled {
			break
		}
		<-time.After(10 * time.Millisecond)
	}
	if !stalled {
		t.Error("the stalled worker's row never said `stalled`: an orchestrator has to read logs to find it")
	}
	var told bool
	for range 200 {
		inbox, err := e.Inbox(ctx, lead)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmtResult(inbox), "has stalled") {
			told = true
			break
		}
		<-time.After(10 * time.Millisecond)
	}
	if !told {
		t.Error("the agent that assigned the work was never told it stalled")
	}
}

// fmtResult flattens a result for a substring check, bodies included.
func fmtResult(r core.Result) string {
	b, _ := json.Marshal(r)
	return string(b)
}

// An approved request is work: with nothing declared, a Dibs-started turn that
// ends while one is open is continued and quoted, the row lists it as owed,
// and reporting it done ends both and tells the requester.
func TestAnApprovedRequestIsOwedUntilReportedDone(t *testing.T) {
	b := newContinuationBoard(t)
	lead := b.e
	res, err := lead.Do(b.ctx, &core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "n-lead-0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	ltok := res["token"].(string)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpAckBoard, Token: ltok}); err != nil {
		t.Fatal(err)
	}
	sent, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpSendMessage, Token: ltok, To: "worker", MsgType: core.MsgRequest, Body: "build stacked C", OpID: "o1"})
	if err != nil {
		t.Fatal(err)
	}
	serial := sent["msg_serial"].(uint64)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRespond, Token: b.token, MsgSerial: serial, Disposition: "approve"}); err != nil {
		t.Fatal(err)
	}
	b.wake(t)
	got := b.stop(t, false)
	if !continued(got) {
		t.Fatal("a turn ended with an approved, undelivered request and was not continued")
	}
	if r, _ := got["reason"].(string); !strings.Contains(r, "build stacked C") {
		t.Errorf("the continuation does not say what is owed: %q", r)
	}
	owes := func() any {
		board, err := b.e.Board(b.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range board["agents"].([]map[string]any) {
			if a["id"] == "worker" {
				return a["owes"]
			}
		}
		return nil
	}
	if o, _ := owes().([]uint64); len(o) != 1 || o[0] != serial {
		t.Errorf("the row's owes is %v, want [%d]", owes(), serial)
	}

	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRespond, Token: b.token, MsgSerial: serial, Disposition: "done"}); err != nil {
		t.Fatalf("done: %v", err)
	}
	if o := owes(); o != nil {
		t.Errorf("still owes %v after reporting it done", o)
	}
	if continued(b.stop(t, false)) {
		t.Error("continued for a request already reported done")
	}
	inbox, err := b.e.Inbox(b.ctx, ltok)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmtResult(inbox), "DONE") {
		t.Errorf("the requester was not told: %s", fmtResult(inbox))
	}
}

// Approval predates done, so an approval older than the window is history,
// not work: an agent with a long past is not woken for it.
func TestAnOldApprovalIsNotAnObligation(t *testing.T) {
	e := &Engine{state: core.NewState("test", core.DefaultLimits())}
	e.state.Messages[7] = &core.Message{
		Serial: 7, From: "lead", To: "worker", Type: core.MsgRequest,
		State: core.MsgStateApproved, TerminalAt: stallT0,
	}
	if n := len(e.obligationsOf("worker", stallT0.Add(time.Hour))); n != 1 {
		t.Fatalf("setup: a fresh approval is not owed (%d)", n)
	}
	if n := len(e.obligationsOf("worker", stallT0.Add(core.ObligationWindow+time.Minute))); n != 0 {
		t.Error("an approval older than the window still reads as owed work")
	}
}

// A declaration nobody has acted on for days is not work in progress: the
// first live board read "working" beside "seen 6d ago". Working needs a recent
// sign of the agent; past that the row says the work is declared.
func TestADeclarationWithNoRecentSignReadsDeclaredNotWorking(t *testing.T) {
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	l := &core.Agent{
		ID: "w", Name: "w", Status: core.StatusDormant,
		Slots:            map[string]core.Slot{"s1": {ID: "s1", Text: "visual identity preview", UpdatedSerial: 3}},
		LastCoordination: time.Now().Add(-6 * 24 * time.Hour),
	}
	e.state.Agents["w"] = l
	if got := e.workStateOf(l); got != "declared" {
		t.Errorf("a six-day-old declaration reads %q", got)
	}
	e.seen["w"] = time.Now()
	if got := e.workStateOf(l); got != "working" {
		t.Errorf("an agent seen just now with open work reads %q", got)
	}
}

// A worker that is working is not woken for its work. Codex reports a turn's
// end and never its start, so the last Stop's record outlives the next turn:
// measured, a backoff wake fired at a worker that had called Dibs two minutes
// earlier and again two minutes later. Any sign of life after the Stop means
// a turn is running.
func TestAWorkerSeenSinceItsLastStopIsNotWokenForItsWork(t *testing.T) {
	if _, err := os.Stat("/usr/bin/touch"); err != nil {
		t.Skip("no /usr/bin/touch on this platform")
	}
	b := newContinuationBoard(t)
	b.declare(t, "member admission runtime", "")
	b.wake(t)
	// Keep the fixture's initial unread-mail wake on its ordinary cooldown.
	// A 1ms cooldown here retries that unacknowledged mail before wake(t) can
	// establish the started turn this test needs to examine.
	b.e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/touch", "{thread}"}, Cooldown: time.Millisecond}})
	for range maxContinuations {
		b.stop(t, false)
	}
	b.stop(t, false) // not continued: the turn has ended
	var ended time.Time
	if _, err := b.e.query(b.ctx, func() core.Result { ended = b.e.turnEnded["worker"]; return nil }); err != nil {
		t.Fatal(err)
	}
	if ended.IsZero() {
		t.Fatal("setup: no turn end recorded")
	}
	// A new turn, started by something Codex does not report, calls Dibs.
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpAckBoard, Token: b.token}); err != nil {
		t.Fatal(err)
	}
	var cwd string
	if _, err := b.e.query(b.ctx, func() core.Result {
		cwd = b.e.state.Agents["worker"].Agent.CWD
		_ = os.Remove(filepath.Join(cwd, contThread)) // a later wake would recreate it
		b.e.seen["worker"] = ended.Add(time.Minute)   // the call landed after the Stop
		b.e.stallTick(ended.Add(continuationBackoff[0] + time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-time.After(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(cwd, contThread)); err == nil {
		t.Error("woke a worker for its work while it was working")
	}
}

// Through the daemon's own sweep, which is the door production takes: an
// approved request survives the sweep that follows approval, so the worker
// can report it done and the requester hears.
func TestTheDaemonsSweepKeepsWorkStillOwed(t *testing.T) {
	b := newContinuationBoard(t)
	res, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "n-lead-0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	ltok := res["token"].(string)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpAckBoard, Token: ltok}); err != nil {
		t.Fatal(err)
	}
	sent, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpSendMessage, Token: ltok, To: "worker", MsgType: core.MsgRequest, Body: "build C", OpID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	serial := sent["msg_serial"].(uint64)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRespond, Token: b.token, MsgSerial: serial, Disposition: "approve"}); err != nil {
		t.Fatal(err)
	}
	// Both sweeps the daemon makes: the boot sweep after a restart, and the
	// periodic one. Each builds its own op, and each must keep it.
	if _, err := b.e.query(b.ctx, func() core.Result {
		b.e.boot(time.Now().Add(20 * time.Minute))
		b.e.sweep(time.Now().Add(25 * time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRespond, Token: b.token, MsgSerial: serial, Disposition: "done", Body: "pr:1700"}); err != nil {
		t.Fatalf("done after the daemon's sweep: %v", err)
	}
}

// An owed request blocked on someone else can be parked. Reported by k7-dev:
// codex-k7-1 owed #2540, the work was finished and publishing was held by an
// owner decision, its declaration said so with `waiting`, and its Stop was
// continued again and again on the request, leaving it a false "done" as the
// only way out. A waiting declaration that names the request in its refs
// (request:<serial>) parks it; one that does not name it leaves it owed.
func TestAWaitingDeclarationThatNamesAnOwedRequestParksIt(t *testing.T) {
	b := newContinuationBoard(t)
	res, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "n-lead-0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	ltok := res["token"].(string)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpAckBoard, Token: ltok}); err != nil {
		t.Fatal(err)
	}
	sent, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpSendMessage, Token: ltok, To: "worker", MsgType: core.MsgRequest, Body: "publish C", OpID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	serial := sent["msg_serial"].(uint64)
	if _, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRespond, Token: b.token, MsgSerial: serial, Disposition: "approve"}); err != nil {
		t.Fatal(err)
	}
	b.wake(t)

	// Waiting, but on something it does not link to the request: still owed.
	if _, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpSetSlot, Token: b.token, SlotID: "s3", Text: "publication held",
		Waiting: "owner decision #1639", RecheckSec: 1800, Refs: []string{"pr:1636"},
	}); err != nil {
		t.Fatal(err)
	}
	got := b.stop(t, false)
	if !continued(got) {
		t.Fatal("setup: an owed request with an unrelated wait was not continued, so nothing below tests parking")
	}
	if r, _ := got["reason"].(string); !strings.Contains(r, fmt.Sprintf("request:%d", serial)) {
		t.Errorf("the continuation does not say how to park the request: %q", r)
	}

	// Naming it parks it.
	if _, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpSetSlot, Token: b.token, SlotID: "s3", Text: "publication held",
		Waiting: "owner decision #1639", RecheckSec: 1800,
		Refs: []string{"pr:1636", fmt.Sprintf("request:%d", serial)},
	}); err != nil {
		t.Fatal(err)
	}
	// Same Dibs-started turn chain, a changed declaration: only parking stops it.
	if got := b.stop(t, false); continued(got) {
		t.Errorf("an owed request parked by a waiting declaration was continued: %q", got["reason"])
	}
	board, err := b.e.Board(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range board["agents"].([]map[string]any) {
		if a["id"] == "worker" && a["work"] != "waiting" {
			t.Errorf("the parked worker's row says work=%v, want waiting", a["work"])
		}
	}
}
