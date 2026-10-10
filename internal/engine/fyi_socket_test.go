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

// Enter through the authenticated bridge offer, write receipt and starting
// hook. A successful kernel write alone cannot prove the model saw a FYI.
func TestCompleteFYISocketConsumesOnlyAfterWrittenOfferAndTurn(t *testing.T) {
	e := New(core.NewState("fyi-socket", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
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
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, SessionID: id + "-session", Nonce: "fyi-socket-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	if _, err := e.HookPoll(ctx, "reader-session", "Stop", "", true, false); err != nil {
		t.Fatal(err)
	}
	id := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["sender"], To: "reader", MsgType: core.MsgNotify, Body: "confirmed-full-fyi"})["msg_serial"].(uint64)
	offer, err := e.SocketOfferFor(ctx, tokens["reader"], "reader-session", "", false)
	if err != nil || !strings.Contains(fmt.Sprint(offer["digest"]), "confirmed-full-fyi") {
		t.Fatalf("setup offer missing body: %v %v", offer, err)
	}
	consumed := func() bool {
		t.Helper()
		res, err := e.query(ctx, func() core.Result { return core.Result{"consumed": e.state.Messages[id].Consumed} })
		if err != nil {
			t.Fatal(err)
		}
		return res["consumed"].(bool)
	}
	if consumed() {
		t.Fatal("building an unwritten offer consumed FYI")
	}
	if _, err := e.SocketOfferFor(ctx, tokens["reader"], "reader-session", offer["offer"].(string), true); err != nil {
		t.Fatal(err)
	}
	if consumed() {
		t.Fatal("unconfirmed socket write consumed FYI")
	}
	if _, err := e.HookPoll(ctx, "reader-session", "UserPromptSubmit", "", false, false); err != nil {
		t.Fatal(err)
	}
	if !consumed() {
		t.Fatal("written full FYI and new-turn evidence did not consume FYI")
	}
	again, err := e.HookPoll(ctx, "reader-session", "UserPromptSubmit", "", false, false)
	if err != nil || strings.Contains(fmt.Sprint(again), "confirmed-full-fyi") {
		t.Fatalf("confirmed FYI repeated: %v %v", again, err)
	}
}
