// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestMilestoneEventAckFirstCallAfterRestart(t *testing.T) {
	led := &memLedger{}
	e := New(core.NewState("t", core.DefaultLimits()), led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	lead := do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "restart-lead", PID: 424242})["token"].(string)
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "restart-worker"})["token"].(string)
	parent := do(&core.Op{Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgRequest, Body: "proof", Milestones: []string{"proof"}})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "approve"})
	if _, err := e.GetMessage(ctx, lead, parent); err != nil {
		t.Fatal(err)
	}
	do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "progress", Milestone: 1})
	var ops []*core.Op
	if _, err := e.query(ctx, func() core.Result { ops = append(ops, led.ops...); return nil }); err != nil {
		t.Fatal(err)
	}
	st := core.NewState("t", core.DefaultLimits())
	for _, op := range ops {
		if _, _, err := st.Apply(op, time.Now()); err != nil {
			t.Fatal("replay:", err)
		}
	}
	cancel()
	<-done
	event := st.Messages[parent].Progress[0].Serial
	serial, consumed := st.Serial, st.Messages[parent].Consumed
	restartedLedger := &memLedger{}
	restarted := New(st, restartedLedger, onePIDDead{dead: 424242})
	if st.Agents["lead"].PID != 424242 || restarted.prober.Alive(424242) {
		t.Fatal("setup: recorded lead PID must be dead after restart")
	}
	if restarted.notices != nil || restarted.seen == nil {
		t.Fatal("setup: restart must have no notices and an initialized seen map")
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartDone := make(chan struct{})
	go func() { defer close(restartDone); restarted.Run(restartCtx) }()
	t.Cleanup(func() { restartCancel(); <-restartDone })
	// The first call is the production operation, with no read or check-in to
	// populate a derived map first. The retained progress comes from replay.
	for range 2 {
		r, err := restarted.Do(restartCtx, &core.Op{Kind: core.OpAckMessage, Token: lead, MsgSerial: event})
		if err != nil || r["state"] != "acked" {
			t.Fatalf("ack after restart: %v %v", r, err)
		}
	}
	if _, err := restarted.query(restartCtx, func() core.Result {
		restarted.sweep(time.Now())
		if st.Agents["lead"].Status != core.StatusActive {
			t.Error("successful derived event ack failed to protect fresh contact from a stale PID")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restartCancel()
	<-restartDone
	// Explicit dismissal now persists via the NEW additive outcome-read prefix,
	// not by changing historical ack's fold. It must survive a daemon restart.
	if st.Serial != serial+1 || len(restartedLedger.ops) != 1 ||
		st.Messages[parent].OutcomeReadAt != event || st.Messages[parent].Consumed != consumed ||
		st.Messages[parent].Progress[0].Review != "" {
		t.Fatal("event ack lost its read marker or consumed/reviewed the parent")
	}
	if restarted.seen["lead"].IsZero() {
		t.Fatal("ack did not touch liveness")
	}
}

func TestMilestoneEventAckDoesNotLedgerReviewOrConsumeParent(t *testing.T) {
	led := &memLedger{}
	e := New(core.NewState("event-ack", core.DefaultLimits()), led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	lead := do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "event-ack-lead"})["token"].(string)
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "event-ack-worker"})["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: lead})
	var events []uint64
	for range 2 {
		parent := do(&core.Op{Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgRequest, Body: "proof", Milestones: []string{"proof"}})["msg_serial"].(uint64)
		do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "approve"})
		if _, err := e.GetMessage(ctx, lead, parent); err != nil {
			t.Fatal(err)
		}
		do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "progress", Milestone: 1})
		if _, err := e.query(ctx, func() core.Result { events = append(events, e.state.Messages[parent].Progress[0].Serial); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	var serial uint64
	var records int
	consumed := map[uint64]bool{}
	if _, err := e.query(ctx, func() core.Result {
		serial = e.state.Serial
		records = len(led.ops)
		for _, m := range e.state.Messages {
			consumed[m.Serial] = m.Consumed
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		do(&core.Op{Kind: core.OpAckMessage, Token: lead, MsgSerial: events[0]})
	}
	if _, err := e.query(ctx, func() core.Result {
		// One durable read, never a fabricated review or a repeat read op.
		if e.state.Serial != serial+1 || len(led.ops) != records+1 {
			t.Error("event ack did not persist exactly one read")
		}
		if len(e.notices["lead"]) != 1 || e.notices["lead"][0].Serial != events[1] {
			t.Errorf("ack cleared unrelated notice: %v", e.notices["lead"])
		}
		for _, m := range e.state.Messages {
			if m.Consumed != consumed[m.Serial] || len(m.Progress) != 1 || m.Progress[0].Review != "" {
				t.Error("event ack consumed or reviewed parent")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementCannotResolvePredecessorsMilestoneEvent(t *testing.T) {
	now := time.Now()
	st := core.NewState("replacement-event", core.DefaultLimits())
	apply := func(op *core.Op) core.Result {
		t.Helper()
		op.V7Semantics = true
		r, _, err := st.Apply(op, now)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "lead", NewToken: "old-lead"})
	apply(&core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "worker"})
	parent := apply(&core.Op{Kind: core.OpSendMessage, Token: "old-lead", To: "worker", MsgType: core.MsgRequest, Body: "old proof", Milestones: []string{"proof"}})["msg_serial"].(uint64)
	apply(&core.Op{Kind: core.OpRespond, Token: "worker", MsgSerial: parent, Disposition: "approve"})
	apply(&core.Op{Kind: core.OpRespond, Token: "worker", MsgSerial: parent, Disposition: "progress", Milestone: 1})
	event := st.Messages[parent].Progress[0].Serial
	delete(st.Agents, "lead") // old row pruned, retained mail still belongs to that incarnation
	apply(&core.Op{Kind: core.OpRegister, Name: "lead", NewToken: "new-lead"})
	if st.Agents["lead"].CreatedSerial <= parent {
		t.Fatal("setup: replacement watermark not advanced")
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	for _, kind := range []string{core.OpAckMessage, core.OpRespond} {
		_, err := e.Do(ctx, &core.Op{Kind: kind, Token: "new-lead", MsgSerial: event, Disposition: "accept", Milestone: 1})
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Hint == "" || strings.Contains(ce.Hint, "read_mail(msg_serial:") {
			t.Fatalf("replacement resolved old parent or got empty hint: %v", err)
		}
	}
}
