// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func capacityFixture(t *testing.T, limit int) (*Engine, context.Context, func(*core.Op) core.Result) {
	t.Helper()
	lim := core.DefaultLimits()
	lim.MaxMailboxDepth = limit
	s := core.NewState("capacity", lim)
	apply := func(op *core.Op) core.Result {
		t.Helper()
		r, _, err := s.Apply(op, time.Now())
		if err != nil {
			t.Fatal("setup", op.Kind, err)
		}
		return r
	}
	for _, name := range []string{"sender", "reader", "other"} {
		apply(&core.Op{Kind: core.OpRegister, Name: name, NewToken: name + "-token", Nonce: name + "-nonce", AgentKind: core.KindPersistent})
		apply(&core.Op{Kind: core.OpAckBoard, Token: name + "-token"})
	}
	e := New(s, &memLedger{}, deadProber{})
	e.SetHumanNotifier(noContactDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	setup := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.query(ctx, func() core.Result { return apply(op) })
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return e, ctx, setup
}

func TestMailboxAnySendDisplacesOldestFYI(t *testing.T) {
	for _, kind := range []string{core.MsgNotify, core.MsgRequest, core.MsgQuestion, core.MsgHandoff} {
		t.Run(kind, func(t *testing.T) {
			e, ctx, apply := capacityFixture(t, 2)
			ids := []uint64{}
			for range 2 {
				ids = append(ids, apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgNotify, Body: "fyi"})["msg_serial"].(uint64))
			}
			r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: kind, Body: "handoff"})
			if err != nil {
				t.Fatalf("%s refused behind FYIs: %v", kind, err)
			}
			got, err := e.query(ctx, func() core.Result {
				return core.Result{"first": e.state.Messages[ids[0]].State, "second": e.state.Messages[ids[1]].State, "new": e.state.Messages[r["msg_serial"].(uint64)].Type}
			})
			if err != nil || got["first"] != core.MsgStateDisplaced || got["second"] == core.MsgStateDisplaced || got["new"] != kind {
				t.Fatalf("wrong displacement %v %v", got, err)
			}
		})
	}
}

func TestMailboxAnnouncedFYIDoesNotBlockAsks(t *testing.T) {
	e, ctx, apply := capacityFixture(t, 2)
	for range 2 {
		id := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgNotify, Body: "fyi"})["msg_serial"].(uint64)
		apply(&core.Op{Kind: core.OpActivityCheckpoint, Token: "reader-token", NotifyAnnounced: []uint64{id}})
	}
	for range 2 {
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgRequest, Body: "work"}); err != nil {
			t.Fatalf("announced FYI consumed capacity: %v", err)
		}
	}
	r, err := e.query(ctx, func() core.Result {
		n := 0
		for _, m := range e.state.Messages {
			if m.To == "reader" && m.Type == core.MsgNotify && !m.Terminal() {
				n++
			}
		}
		return core.Result{"kept": n}
	})
	if err != nil || r["kept"] != 2 {
		t.Fatalf("exempt FYIs were displaced to make asks fit: %v %v", r, err)
	}
}

func TestMailboxFullAsksRaiseContactAndCorrectiveHint(t *testing.T) {
	e, ctx, apply := capacityFixture(t, 2)
	for range 2 {
		apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgRequest, Body: "PRIVATE EXISTING ASK"})
	}
	_, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgHandoff, Body: "PRIVATE REFUSED HANDOFF"})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_MAILBOX_FULL" {
		t.Fatalf("want full: %v", err)
	}
	if !strings.Contains(ce.Hint, "send(") || !strings.Contains(ce.Hint, "human") {
		t.Errorf("no corrective call: %s", ce.Hint)
	}
	b, err := e.Board(ctx)
	alerts, _ := json.Marshal(b["contact_alerts"])
	if err != nil || !strings.Contains(string(alerts), "reader") {
		t.Fatalf("no recipient contact alert: %v %v", b, err)
	}
	if strings.Contains(fmt.Sprint(b), "PRIVATE") {
		t.Fatal("contact leaks participant content")
	}
}

func TestMailboxBulkAckOnlyOwnAnnouncedFYIsThroughPrefix(t *testing.T) {
	e, ctx, apply := capacityFixture(t, 256)
	ids := []uint64{}
	for _, to := range []string{"reader", "reader", "other", "reader"} {
		id := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: to, MsgType: core.MsgNotify, Body: "fyi"})["msg_serial"].(uint64)
		ids = append(ids, id)
		apply(&core.Op{Kind: core.OpActivityCheckpoint, Token: to + "-token", NotifyAnnounced: []uint64{id}})
	}
	unseen := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgNotify, Body: "unannounced"})["msg_serial"].(uint64)
	ask := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgRequest, Body: "ask"})["msg_serial"].(uint64)
	var op core.Op
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"kind":"ack","notify_through_serial":%d}`, ids[2])), &op); err != nil {
		t.Fatal(err)
	}
	op.Token = "reader-token"
	r, err := e.Do(ctx, &op)
	if err != nil {
		t.Fatalf("bulk ack refused: %v", err)
	}
	if r["consumed_count"] != 2 {
		t.Fatalf("bulk count: %v", r)
	}
	got, err := e.query(ctx, func() core.Result {
		out := core.Result{}
		for _, id := range append(ids, unseen, ask) {
			out[fmt.Sprint(id)] = e.state.Messages[id].Consumed
		}
		return out
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range append(ids, unseen, ask) {
		if got[fmt.Sprint(id)] != (i < 2) {
			t.Fatalf("selection changed %d: %v", id, got)
		}
	}
}

func TestMailboxNearCapacityVisibleOnCheckInAndBoard(t *testing.T) {
	e, ctx, apply := capacityFixture(t, 5)
	for range 4 {
		apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgRequest, Body: "work"})
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "reader-token"})
	if err != nil {
		t.Fatal(err)
	}
	if r["mailbox_capacity"] == nil {
		t.Errorf("check_in lacks capacity warning: %v", r)
	}
	b, err := e.Board(ctx)
	if err != nil || !strings.Contains(fmt.Sprint(b), "mailbox_capacity") {
		t.Fatalf("board lacks capacity warning: %v %v", b, err)
	}
}

func TestMailboxAckOfMissingEnvelopeOffersHistory(t *testing.T) {
	e, ctx, _ := capacityFixture(t, 2)
	_, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "reader-token", MsgSerial: 1 << 40})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_NO_MESSAGE" ||
		!strings.Contains(ce.Hint, "inbox()") || !strings.Contains(ce.Hint, "mail_history()") {
		t.Fatalf("missing ack has no retention recovery: %v", err)
	}
}

func TestMailboxBulkAckUsesSingleRateUnit(t *testing.T) {
	e, ctx, apply := capacityFixture(t, 256)
	var last uint64
	for range 120 {
		last = apply(&core.Op{
			Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgNotify, Body: "FYI",
		})["msg_serial"].(uint64)
		apply(&core.Op{Kind: core.OpActivityCheckpoint, Token: "reader-token", NotifyAnnounced: []uint64{last}})
	}
	if err := e.SetRateTokens(ctx, "reader", 1); err != nil {
		t.Fatal(err)
	}
	var op core.Op
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"kind":"ack","notify_through_serial":%d}`, last)), &op); err != nil {
		t.Fatal(err)
	}
	op.Token = "reader-token"
	r, err := e.Do(ctx, &op)
	if err != nil || r["consumed_count"] != 120 {
		t.Fatalf("one rate token did not consume 120 FYIs: %v %v", r, err)
	}
}

func TestMailboxContactStaysOnReadAndResolvesWhenSpaceFreed(t *testing.T) {
	e, ctx, apply := capacityFixture(t, 2)
	var source uint64
	for range 2 {
		source = apply(&core.Op{
			Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgRequest, Body: "work",
		})["msg_serial"].(uint64)
	}
	for range 2 {
		_, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgHandoff, Body: "next",
		})
		if !errors.Is(err, core.ErrMailboxFull) {
			t.Fatalf("setup: full send: %v", err)
		}
	}
	if _, err := e.GetMessage(ctx, "reader-token", source); err != nil {
		t.Fatal(err)
	}
	r, err := e.query(ctx, func() core.Result {
		open := 0
		for _, c := range e.state.Contacts {
			if c.ResolvedAt.IsZero() {
				open++
			}
		}
		return core.Result{"open": open}
	})
	if err != nil || r["open"] != 1 {
		t.Fatalf("read resolved a full mailbox or repeated refusal duplicated contact: %v %v", r, err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: "reader-token", MsgSerial: source, Disposition: "decline",
	}); err != nil {
		t.Fatal(err)
	}
	b, err := e.Board(ctx)
	if err != nil || b["contact_alerts"] != nil {
		t.Fatalf("available capacity left contact unresolved: %v %v", b, err)
	}
}
