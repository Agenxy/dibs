// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import "testing"

func TestOutgoingUnreadAskShowsAgeUntilRecipientAwareness(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"question", "request", "handoff"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				srv, _, _ := restartableQueueServer(t, t.TempDir())
				call := func(name string, a map[string]any) map[string]any {
					t.Helper()
					r := toolCallOn(t, srv, version, name, a)
					if r["code"] != nil || r["error"] != nil {
						t.Fatalf("setup %s: %v", name, r)
					}
					return r
				}
				sender := call("register", map[string]any{"name": "sender", "nonce": "age-sender"})
				worker := call("register", map[string]any{"name": "worker", "nonce": "age-worker"})
				for _, a := range []map[string]any{sender, worker} {
					call("check_in", map[string]any{"token": a["token"]})
				}
				id := call("send", map[string]any{"token": sender["token"], "to": "worker", "type": kind, "body": "unread age"})["msg_serial"]
				for _, full := range []bool{false, true} {
					m := call("read_mail", map[string]any{"token": sender["token"], "msg_serial": id, "include_body": full})["message"].(map[string]any)
					if age, ok := m["undelivered_for_seconds"].(float64); !ok || age < 0 {
						t.Fatalf("outgoing unread ask omitted actual undelivered age: %v", m)
					}
					if m["delivered_serial"] != nil {
						t.Fatal("sender read falsely marked recipient awareness")
					}
				}
				call("inbox", map[string]any{"token": worker["token"]})
				m := call("read_mail", map[string]any{"token": sender["token"], "msg_serial": id})["message"].(map[string]any)
				if _, ok := m["undelivered_for_seconds"]; ok {
					t.Fatalf("undelivered age survived recipient awareness: %v", m)
				}
			})
		}
	}
}
