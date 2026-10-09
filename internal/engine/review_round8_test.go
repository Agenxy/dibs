// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// R8-1: blocking mail outstanding at boot gets its wake decision again. A
// deferred wake was a timer and a restart lost it; boot rebuilt notices and
// primed the socket cache and scheduled nothing for mail already in the ledger.
func TestBlockingMailOutstandingAtBootIsDecidedAgain(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	mk := func() *Engine {
		st := core.NewState("test", core.DefaultLimits())
		st.Agents["cc"] = &core.Agent{
			ID: "cc", Name: "cc", Status: core.StatusActive, SessionID: sid,
			Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
			Slots: map[string]core.Slot{},
		}
		st.Messages[5] = &core.Message{
			Serial: 5, From: "asker", To: "cc", Type: core.MsgQuestion, State: core.MsgStatePending,
		}
		return New(st, &memLedger{}, deadProber{})
	}
	e := mk()
	if !e.hasBlockingMail("cc") {
		t.Fatal("setup: the fixture holds no blocking mail, so nothing below proves anything")
	}
	if n := e.rearmDeferredWakes(); n != 1 {
		t.Fatalf("rearmed %d agent(s), want 1", n)
	}
	e.wakers.mu.Lock()
	armed := e.wakers.deferred["cc"] != nil
	if armed {
		e.wakers.deferred["cc"].Stop()
	}
	e.wakers.mu.Unlock()
	if armed {
		t.Fatal("boot event created a timer without a failed delivery")
	}

	// Run wires the reconnect decision before the first request.

	e = mk()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	// Boot runs before the loop serves, so a served query means boot is done.
	if _, err := e.query(ctx, func() core.Result { return core.Result{"ok": true} }); err != nil {
		t.Fatal(err)
	}
	e.wakers.mu.Lock()
	armed = e.wakers.deferred["cc"] != nil
	e.wakers.mu.Unlock()
	if armed {
		t.Fatal("Run created an outstanding-mail polling timer")
	}
}

// R8-2: a return to a thread bound earlier is the thread to wake.
func TestReturningToAnEarlierThreadWakesThatThread(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("test", core.DefaultLimits())
	t0 := t0Engine()
	for i, o := range []*core.Op{
		{Kind: core.OpRegister, Name: "returning", NewToken: "tok", AgentKind: core.KindPersistent, Nonce: "n", Agent: &core.AgentInfo{Harness: "Codex"}, SessionAlias: a, V7Semantics: true},
		{Kind: core.OpAckBoard, Token: "tok", SessionAlias: b, V7Semantics: true},
		{Kind: core.OpAckBoard, Token: "tok", SessionAlias: a, V7Semantics: true},
	} {
		if _, _, err := st.Apply(o, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal("setup:", err)
		}
	}
	l := st.Agents["returning"]
	if !l.HoldsSession(a) || !l.HoldsSession(b) {
		t.Fatal("setup: the agent does not hold both threads, so the choice below proves nothing")
	}
	if got := threadIDOf(l); got != a {
		t.Errorf("the wake would resume %s; the harness last reported %s. A real session "+
			"starts that is not the one holding the mail, and the wake logs as a success", got, a)
	}
}

// A second socket miss is still not a failed delivery and cannot poll.
func TestASecondSocketMissDoesNotCreateAPollingTimer(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	st := core.NewState("test", core.DefaultLimits())
	st.Agents["cc"] = &core.Agent{
		ID: "cc", Name: "cc", Status: core.StatusActive, SessionID: sid,
		Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
		Slots: map[string]core.Slot{},
	}
	st.Messages[5] = &core.Message{
		Serial: 5, From: "asker", To: "cc", Type: core.MsgQuestion, State: core.MsgStatePending,
	}
	e := New(st, &memLedger{}, deadProber{})
	e.peers.mu.Lock()
	e.peers.at = time.Now()
	e.peers.live = map[string]peerwake.Session{}
	e.peers.mu.Unlock()
	e.retryWakeDecision("cc") // the retry itself misses again
	e.wakers.mu.Lock()
	armed := e.wakers.deferred["cc"] != nil
	if armed {
		e.wakers.deferred["cc"].Stop()
	}
	e.wakers.mu.Unlock()
	if armed {
		t.Fatal("missing socket created an unbounded polling timer")
	}
}
