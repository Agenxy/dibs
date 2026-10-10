// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestMailboxCursorAndLimitsRefuseBeforeDelivery(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, eng := boundedMailServer(t)
			first := waitingCall(t, srv, version, "inbox", map[string]any{"token": "reader-token"})
			cursor, _ := first["next_cursor"].(string)
			if cursor == "" {
				t.Fatal("retained backlog has no cursor")
			}
			fresh := waitingCall(t, srv, version, "send", map[string]any{"token": "sender-token", "to": "reader", "type": "notify", "body": "untouched"})
			id := uint64(fresh["msg_serial"].(float64))
			for _, method := range []string{"inbox", "check_in"} {
				for _, args := range []map[string]any{
					{"token": "reader-token", "cursor": "not-a-cursor"},
					{"token": "sender-token", "cursor": cursor},
					{"token": "reader-token", "limit": 0},
					{"token": "reader-token", "limit": -1},
					{"token": "reader-token", "limit": 9},
				} {
					out := rpc(t, srv, version, "tools/call", map[string]any{"name": method, "arguments": args})
					r, ok := out["result"].(map[string]any)
					if !ok || r["isError"] != true {
						t.Fatalf("invalid mailbox selector succeeded: %v", out)
					}
					if m := mailboxReceipt(t, eng, id); m.State != core.MsgStatePending || m.DeliveredAt != 0 {
						t.Fatal("refusal delivered an omitted envelope")
					}
				}
			}
		})
	}
}
