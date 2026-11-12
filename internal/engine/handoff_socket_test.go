// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestHandoffSocketWithdrawalNeedsConfirmedPresentation(t *testing.T) {
	for _, stage := range []string{"offered", "written", "confirmed"} {
		t.Run(stage, func(t *testing.T) {
			e := New(core.NewState("handoff-socket", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { e.Run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			do := func(op *core.Op) core.Result {
				t.Helper()
				res, err := e.Do(ctx, op)
				if err != nil {
					t.Fatalf("setup %s: %v", op.Kind, err)
				}
				return res
			}
			tokens := map[string]string{}
			for _, id := range []string{"reader", "sender"} {
				tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, SessionID: id + "-session", Nonce: "handoff-socket-" + id})["token"].(string)
				do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
			}
			if _, err := e.HookPoll(ctx, "reader-session", "Stop", "", true, false); err != nil {
				t.Fatal(err)
			}
			id := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["sender"], To: "reader", MsgType: core.MsgHandoff, Body: "complete-socket-handoff"})["msg_serial"].(uint64)
			offer, err := e.SocketOfferFor(ctx, tokens["reader"], "reader-session", "", false)
			if err != nil || !strings.Contains(fmt.Sprint(offer["digest"]), "complete-socket-handoff") {
				t.Fatalf("setup offer: %v %v", offer, err)
			}
			if stage != "offered" {
				if _, err := e.SocketOfferFor(ctx, tokens["reader"], "reader-session", offer["offer"].(string), true); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "confirmed" {
				// Model activity confirms the earlier complete offer without
				// independently reading or presenting its envelope.
				if _, err := e.SpaceRead(ctx, tokens["reader"], "missing-space", 1); err == nil {
					t.Fatal("setup: nonexistent space existed")
				}
			}
			res, err := e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: tokens["sender"], MsgSerial: id, Disposition: "withdraw"})
			if stage == "confirmed" {
				var refusal *core.Error
				if !errors.As(err, &refusal) || refusal.Code != "E_MSG_FINAL" {
					t.Fatalf("confirmed handoff could be withdrawn: %v %v", res, err)
				}
			} else if err != nil || res["state"] != core.MsgStateWithdrawn {
				t.Fatalf("socket offer/write invented a read: %v %v", res, err)
			}
		})
	}
}
