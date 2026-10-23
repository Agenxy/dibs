// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestMailboxBulkAckThroughMCPAndBothProtocols(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			eng := srv.Config.Handler.(*Server).eng
			reader := waitingCall(t, srv, version, "register", map[string]any{"name": "reader", "session_id": "reader-session"})
			sender := waitingCall(t, srv, version, "register", map[string]any{"name": "sender"})
			var owed []uint64
			for range 2 {
				ask := waitingCall(t, srv, version, "send", map[string]any{"token": sender["token"], "to": "reader", "type": "request", "body": "accepted work"})
				id := uint64(ask["msg_serial"].(float64))
				owed = append(owed, id)
				waitingCall(t, srv, version, "respond", map[string]any{"token": reader["token"], "msg_serial": id, "disposition": "approve"})
			}
			for range 3 {
				waitingCall(t, srv, version, "send", map[string]any{"token": sender["token"], "to": "reader", "type": "notify", "body": strings.Repeat("oversized FYI ", 2000)})
			}
			waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "Stop"})
			before, err := eng.AllMessages(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			announced, already := 0, 0
			var cutoff uint64
			for _, m := range before["messages"].([]*core.Message) {
				if m.Serial > cutoff {
					cutoff = m.Serial
				}
				if m.Type == core.MsgNotify {
					if m.Consumed {
						already++
					} else if m.NotifyAnnouncedAt != 0 {
						announced++
					}
				}
			}
			if announced == 0 {
				t.Fatal("setup: hook did not announce any FYIs")
			}
			if err := eng.SetRateTokens(context.Background(), "reader", 1); err != nil {
				t.Fatal(err)
			}
			r := waitingCall(t, srv, version, "ack", map[string]any{"token": reader["token"], "notify_through_serial": cutoff})
			if r["consumed_count"] != float64(announced) {
				t.Fatalf("bulk ack failed: %v", r)
			}
			all, err := eng.AllMessages(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			consumed := 0
			for _, m := range all["messages"].([]*core.Message) {
				if m.Type == core.MsgNotify && m.Consumed {
					consumed++
				}
			}
			if consumed != announced+already {
				t.Fatalf("consumed unannounced FYIs: %d", consumed)
			}
			for _, id := range owed {
				if m := mailboxReceipt(t, eng, id); m.State != core.MsgStateApproved || !m.Owed(m.TerminalAt) {
					t.Fatal("bulk changed accepted request")
				}
			}
			if err := eng.SetRateTokens(context.Background(), "reader", 1); err != nil {
				t.Fatal(err)
			}
			again := waitingCall(t, srv, version, "ack", map[string]any{"token": reader["token"], "notify_through_serial": cutoff})
			if again["consumed_count"] != float64(0) {
				t.Fatalf("bulk not idempotent: %v", again)
			}
		})
	}
}
