// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

var stallT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

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
		Waiting: "owner decision #1639", Refs: []string{"pr:1636"},
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
		Waiting: "owner decision #1639",
		Refs:    []string{"pr:1636", fmt.Sprintf("request:%d", serial)},
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
