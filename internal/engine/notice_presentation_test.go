// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestStopBodyDeliveryDoesNotRepeatOnAuthenticatedPull(t *testing.T) {
	e := New(core.NewState("notice-presentation", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("%s setup: %v", op.Kind, err)
		}
		return r
	}
	tokens := map[string]string{}
	for _, id := range []string{"asker", "answerer"} {
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, SessionID: id + "-session", Nonce: "notice-presentation-fixture-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	s := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["asker"], To: "answerer", MsgType: core.MsgQuestion, Body: "may I?"})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: tokens["answerer"], MsgSerial: s, Disposition: "answer", Body: "yes"})
	stop := func(want bool) {
		t.Helper()
		r, err := e.HookPoll(ctx, "asker-session", "Stop", "", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := r["reason"] != nil; got != want {
			t.Fatalf("Stop presentation = %v, want %v: %v", got, want, r)
		}
	}
	stop(true)
	stop(false)
	r, err := e.Inbox(ctx, tokens["asker"])
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r["agent_updates"].([]string); len(got) != 0 {
		t.Fatalf("already quoted Stop answer repeated on inbox: %v", r)
	}
	if _, err := e.query(ctx, func() core.Result {
		for key := range e.noticePresented {
			e.noticePresented[key] = time.Now().Add(-AnnounceRetry - time.Second)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stop(false) // cadence expiration cannot unread delivered words
	r = do(&core.Op{Kind: core.OpAckBoard, Token: tokens["asker"]})
	if got, _ := r["agent_updates"].([]string); len(got) != 0 {
		t.Fatalf("already read answer repeated on check_in: %v", r)
	}
	stop(false)
}
