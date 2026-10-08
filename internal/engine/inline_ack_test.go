// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestNewerReviewAckNamesItsPrefixAndIsIdempotent(t *testing.T) {
	led := &memLedger{}
	e := New(core.NewState("review-ack", core.DefaultLimits()), led, deadProber{})
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
	tokens := map[string]string{}
	for _, id := range []string{"lead", "worker"} {
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "review-ack-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	n := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker", MsgType: core.MsgRequest, Body: "work", Milestones: []string{"proof"}})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: n, Disposition: "approve"})
	do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: n, Disposition: "progress", Milestone: 1})
	do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: n, Disposition: "done"})
	for _, body := range []string{"older flag", "newer flag"} {
		do(&core.Op{Kind: core.OpRespond, Token: tokens["lead"], MsgSerial: n, Disposition: "flag", Milestone: 1, Body: body})
	}
	var old, newest uint64
	_, err := e.query(ctx, func() core.Result {
		p := e.state.Messages[n].Progress
		old, newest = p[len(p)-2].Serial, p[len(p)-1].Serial
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := do(&core.Op{Kind: core.OpAckMessage, Token: tokens["worker"], MsgSerial: newest})
	covered, ok := result["also_read"].([]uint64)
	if !ok || len(covered) != 1 || covered[0] != old {
		t.Errorf("cumulative ack was silent: %v", result)
	}
	var serial uint64
	var records int
	_, err = e.query(ctx, func() core.Result {
		serial, records = e.state.Serial, len(led.ops)
		if e.state.Messages[n].ReviewReadAt != newest {
			t.Error("ack did not persist recipient prefix")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result = do(&core.Op{Kind: core.OpAckMessage, Token: tokens["worker"], MsgSerial: newest})
	if result["also_read"] != nil {
		t.Errorf("already-read prefix reported again: %v", result)
	}
	_, err = e.query(ctx, func() core.Result {
		if e.state.Serial != serial || len(led.ops) != records {
			t.Error("second ack advanced serial or appended ledger")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(replayed(t, ctx, e, led), &memLedger{}, deadProber{})
	if len(restarted.receivedReviews("worker")) != 0 {
		t.Error("acked review prefix redelivered after replay")
	}
}

func TestProgressAckPersistsItsPrefixAndDoesNotRepeat(t *testing.T) {
	led := &memLedger{}
	e := New(core.NewState("progress-ack", core.DefaultLimits()), led, deadProber{})
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
	lead := do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "progress-ack-lead"})["token"].(string)
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "progress-ack-worker"})["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: lead})
	parent := do(&core.Op{
		Kind: core.OpSendMessage, Token: lead, To: "worker",
		MsgType: core.MsgRequest, Body: "work",
	})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "approve"})
	if _, err := e.GetMessage(ctx, lead, parent); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"older report", "newer report"} {
		do(&core.Op{
			Kind: core.OpRespond, Token: worker, MsgSerial: parent,
			Disposition: "progress", Body: body,
		})
	}
	var older, newer uint64
	if _, err := e.query(ctx, func() core.Result {
		p := e.state.Messages[parent].Progress
		older, newer = p[0].Serial, p[1].Serial
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := do(&core.Op{Kind: core.OpAckMessage, Token: lead, MsgSerial: newer})
	covered, ok := r["also_read"].([]uint64)
	if !ok || len(covered) != 1 || covered[0] != older {
		t.Errorf("cumulative progress ack did not name its prefix: %v", r)
	}
	// A fresh encrypted MCP restart is covered separately; replay here also
	// proves that no ephemeral notice clear is being mistaken for a read.
	restarted := New(replayed(t, ctx, e, led), &memLedger{}, deadProber{})
	if strings.Contains(fmt.Sprint(restarted.pendingNotices("lead")), "reports progress") {
		t.Error("explicitly dismissed progress report returned after replay")
	}
}
