// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

func TestAuthenticatedHumanStreamCarriesWithdrawalAndLatePostCleanup(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	session := h.session()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	human, _, err := h.eng.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup human:", err)
	}
	reg, err := h.eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "cleanup-sender"})
	if err != nil || reg["token"] == nil {
		t.Fatalf("setup sender: %v %v", reg, err)
	}
	feed := h.attach(ctx, session)
	sent, err := h.eng.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: reg["token"].(string), To: human, MsgType: core.MsgQuestion, Body: "question",
	})
	if err != nil || sent["ok"] != true {
		t.Fatalf("setup send: %v %v", sent, err)
	}
	serial := sent["msg_serial"].(uint64)
	next := func(ch <-chan engine.HumanNotice) engine.HumanNotice {
		t.Helper()
		select {
		case notice := <-ch:
			return notice
		case <-ctx.Done():
			t.Fatal("authenticated stream did not deliver cleanup")
			return engine.HumanNotice{}
		}
	}
	if n := next(feed); n.Serial != serial || n.Type != core.MsgQuestion {
		t.Fatalf("setup notice: %+v", n)
	}
	res, err := h.eng.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: reg["token"].(string), MsgSerial: serial,
		Disposition: "withdraw", Body: "answered elsewhere",
	})
	if err != nil || res["state"] != core.MsgStateWithdrawn {
		t.Fatalf("withdrawal: %v %v", res, err)
	}
	expect := func(ch <-chan engine.HumanNotice) {
		t.Helper()
		n := next(ch)
		if n.Serial != 0 || n.Cleanup == nil || n.Cleanup.Node != h.eng.NodeID() ||
			!reflect.DeepEqual(n.Cleanup.Serials, []uint64{serial}) {
			t.Fatalf("cleanup envelope: %+v", n)
		}
	}
	expect(feed)
	code, out := h.post("/api/human/delivery", map[string]string{"Authorization": "Bearer " + session},
		map[string]any{"serial": serial, "state": "posted"})
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("late post receipt: %d %v", code, out)
	}
	expect(feed)
	code, out = h.post("/api/human/delivery", map[string]string{"Authorization": "Bearer " + session},
		map[string]any{"serial": serial, "state": "cleanup_requested"})
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("derived cleanup receipt: %d %v", code, out)
	}
	// A newly attached relay rebuilds retained cleanup even after missing the
	// live event. No old notice is presented as another question.
	feed2 := h.attach(ctx, session)
	expect(feed2)
}
