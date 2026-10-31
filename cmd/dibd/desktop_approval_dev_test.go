// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build dibdev

package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The actual native answer entry point must reach humanauth.Check. Its existing
// compile-time development mock replaces only the sensor, never the setting,
// message lookup, effect classification or writer admission.
func TestNativePrivilegedApprovalCanceledByPresenceStaysPending(t *testing.T) {
	t.Setenv("DIBS_PRESENCE_MOCK", "declined")
	eng, ctx := testEngine(t)
	r, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "presence-asker", Nonce: "presence-asker"})
	if err != nil || r["token"] == nil {
		t.Fatalf("registration setup: %v %v", r, err)
	}
	token := r["token"].(string)
	if _, err := eng.Configure(ctx, token, "human.confirm_privileged_desktop", "true"); err != nil {
		t.Fatalf("enable setup: %v", err)
	}
	sent, err := eng.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: "human",
		MsgType: core.MsgRequest, Grant: core.RoleCoordinator, Body: "review this typed grant",
	})
	if err != nil || sent["msg_serial"] == nil {
		t.Fatalf("typed request setup: %v %v", sent, err)
	}
	serial := sent["msg_serial"].(uint64)
	if err := eng.AnswerNativeHuman(ctx, eng.NodeID(), serial, "approve", "", ""); err == nil {
		t.Fatal("native privileged approval bypassed the canceled presence check")
	}
	view, err := eng.NativeHumanMessage(ctx, eng.NodeID(), serial)
	if err != nil || !view.Pending {
		t.Fatalf("cancel decided the request: %+v %v", view, err)
	}
}
