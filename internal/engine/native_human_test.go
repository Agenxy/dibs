// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestNativeHumanAnswerIsAtomicAndBoardBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	e := New(core.NewState("native-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "native-sender"})
	if err != nil || reg["token"] == nil {
		t.Fatalf("register: %v %v", reg, err)
	}
	token := reg["token"].(string)
	send := func(kind string) uint64 {
		t.Helper()
		r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: human, MsgType: kind, Body: "current body"})
		if err != nil || r["msg_serial"] == nil {
			t.Fatalf("send: %v %v", r, err)
		}
		return r["msg_serial"].(uint64)
	}
	serial := send(core.MsgQuestion)
	for _, node := range []string{"", "another-node"} {
		if _, err := e.NativeHumanMessage(ctx, node, serial); err == nil {
			t.Fatal("foreign board read accepted")
		}
		if err := e.AnswerNativeHuman(ctx, node, serial, "answer", "wrong board", ""); err == nil {
			t.Fatal("foreign board answer accepted")
		}
	}
	var wg sync.WaitGroup
	success := make(chan struct{}, 16)
	for range 16 {
		wg.Go(func() {
			if e.AnswerNativeHuman(ctx, "native-node", serial, "answer", "one answer", "") == nil {
				success <- struct{}{}
			}
		})
	}
	wg.Wait()
	close(success)
	if len(success) != 1 {
		t.Fatalf("recorded %d answers, want exactly one", len(success))
	}
	view, err := e.NativeHumanMessage(ctx, "native-node", serial)
	if err != nil || view.Pending || view.State != core.MsgStateAnswered {
		t.Fatalf("answered state: %+v %v", view, err)
	}
	request := send(core.MsgRequest)
	if err := e.AnswerNativeHuman(ctx, "native-node", request, "approve", "yes", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.AnswerNativeHuman(ctx, "native-node", request, "deny", "late", ""); err == nil {
		t.Fatal("second decision on approved request accepted")
	}
	if err := e.AnswerNativeHuman(ctx, "native-node", request, "comment", "stale reply", "stale-comment"); err == nil {
		t.Fatal("stale window sent a comment on an already approved request")
	}
	view, err = e.NativeHumanMessage(ctx, "native-node", request)
	if err != nil || view.Pending || view.State != core.MsgStateApproved {
		t.Fatalf("approved state: %+v %v", view, err)
	}
	expired := send(core.MsgQuestion)
	_, err = e.query(ctx, func() core.Result { e.state.Messages[expired].Deadline = time.Now().Add(-time.Minute); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AnswerNativeHuman(ctx, "native-node", expired, "answer", "too late", ""); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired answer accepted or misreported: %v", err)
	}
	view, err = e.NativeHumanMessage(ctx, "native-node", expired)
	if err != nil || view.Pending || view.State != "expired" {
		t.Fatalf("expired view: %+v %v", view, err)
	}
}

func TestNativeHumanCommentsAreIdempotentAndNeverDecide(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	e := New(core.NewState("comment-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "requester", Nonce: "comment-requester"})
	if err != nil || r["token"] == nil {
		t.Fatalf("register: %v %v", r, err)
	}
	sent, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: r["token"].(string), To: human, MsgType: core.MsgRequest, Body: "request"})
	if err != nil || sent["msg_serial"] == nil {
		t.Fatalf("send: %v %v", sent, err)
	}
	serial := sent["msg_serial"].(uint64)
	for range 2 {
		if err := e.AnswerNativeHuman(ctx, "comment-node", serial, "comment", "more detail", "same-submission"); err != nil {
			t.Fatal(err)
		}
	}
	_, err = e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if m.State != core.MsgStatePending && m.State != core.MsgStateDelivered {
			t.Errorf("comment decided: %s", m.State)
		}
		count := 0
		for _, m := range e.state.Messages {
			if m.From == human && strings.Contains(m.Body, "more detail") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("retry recorded %d comments", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// One banner click walks every waiting question and request, oldest first,
// and wraps; a settled, expired or plain notify never appears in the walk.
func TestNativeHumanQueueWalksTheHumansWaitingMail(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	e := New(core.NewState("queue-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", Nonce: "queue-asker"})
	if err != nil || r["token"] == nil {
		t.Fatalf("register: %v %v", r, err)
	}
	send := func(kind string) uint64 {
		t.Helper()
		sent, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: r["token"].(string), To: human, MsgType: kind, Body: kind})
		if err != nil || sent["msg_serial"] == nil {
			t.Fatalf("send: %v %v", sent, err)
		}
		return sent["msg_serial"].(uint64)
	}
	first, fyi, second, expired, third := send(core.MsgQuestion), send(core.MsgNotify), send(core.MsgRequest), send(core.MsgQuestion), send(core.MsgQuestion)
	if _, err := e.query(ctx, func() core.Result { e.state.Messages[expired].Deadline = time.Now().Add(-time.Minute); return nil }); err != nil {
		t.Fatal(err)
	}
	queue := func(serial uint64) NativeHumanQueue {
		t.Helper()
		q, err := e.NativeHumanQueue(ctx, "queue-node", serial)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	for serial, want := range map[uint64]NativeHumanQueue{
		first:  {Position: 1, Total: 3, Next: second, Previous: third},
		second: {Position: 2, Total: 3, Next: third, Previous: first},
		third:  {Position: 3, Total: 3, Next: first, Previous: second},
		fyi:    {Total: 3, Next: second, Previous: first},
	} {
		if got := queue(serial); got != want {
			t.Errorf("queue around #%d = %+v, want %+v", serial, got, want)
		}
	}
	if err := e.AnswerNativeHuman(ctx, "queue-node", second, "approve", "", ""); err != nil {
		t.Fatal(err)
	}
	// Just answered: the window opens the next one, not the one it left.
	if got := queue(second); got != (NativeHumanQueue{Total: 2, Next: third, Previous: first}) {
		t.Errorf("after answering #%d: %+v", second, got)
	}
	if err := e.AnswerNativeHuman(ctx, "queue-node", first, "answer", "done", ""); err != nil {
		t.Fatal(err)
	}
	if got := queue(third); got != (NativeHumanQueue{Position: 1, Total: 1}) {
		t.Errorf("last one waiting has nowhere to walk: %+v", got)
	}
	if got := queue(first); got.Next != third {
		t.Errorf("answering the second-last does not open the last: %+v", got)
	}
	if got, err := e.NativeHumanQueue(ctx, "another-node", third); err != nil || got != (NativeHumanQueue{}) {
		t.Errorf("another board's queue: %+v %v", got, err)
	}
}
