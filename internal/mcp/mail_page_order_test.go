// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"testing"
)

// k7-dev, 2026-10-14: three notifies sent on 2026-10-06 were first shown,
// and so first delivered, eight days later. Owed work ranked above new mail,
// a page holds at most eight items, and k7-dev always had requests it had
// read and not yet answered, so page one never reached the notifies. Mail
// waiting for attention is now one rank, oldest first.
func TestAnOldUnreadNotifyIsNotStarvedByOwedRequests(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			reader := toolCall(t, srv, "register", map[string]any{"name": "reader"})
			sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})
			old := waitingCall(t, srv, version, "send", map[string]any{
				"token": sender["token"], "to": "reader", "type": "notify", "body": "OLD-NOTIFY",
			})
			oldSerial := old["msg_serial"].(float64)
			for i := range 9 {
				r := waitingCall(t, srv, version, "send", map[string]any{
					"token": sender["token"], "to": "reader", "type": "request", "body": fmt.Sprint("work ", i),
				})
				// Read and owed: the agent knows about it and has not answered.
				waitingCall(t, srv, version, "read_mail", map[string]any{"token": reader["token"], "msg_serial": r["msg_serial"]})
			}
			page := waitingCall(t, srv, version, "inbox", map[string]any{"token": reader["token"]})
			items, _ := page["inbox"].([]any)
			if len(items) < 8 {
				t.Fatalf("setup: page one holds %d items, so it was never full: %v", len(items), page)
			}
			for _, raw := range items {
				if item, _ := raw.(map[string]any); item["serial"] == oldSerial {
					return
				}
			}
			t.Fatalf("the oldest unread notify was pushed off page one by owed requests: %v", page["mail_hint"])
		})
	}
}
