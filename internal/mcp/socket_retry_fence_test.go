// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

func TestFailedSocketOfferCannotRetryForUnrelatedNewMail(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			const session = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"
			worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "session_id": session})
			sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})
			token := worker["token"]
			send := func(body string) any {
				return toolCall(t, srv, "send", map[string]any{"token": sender["token"], "to": "worker", "type": "question", "body": body})["msg_serial"]
			}
			idle := func() {
				toolCall(t, srv, "hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
			}
			read := func(extra map[string]any) map[string]any {
				meta := map[string]any{"com.dibs/token": token, "com.dibs/session": session, "com.dibs/socket_offer": true}
				for k, v := range extra {
					meta[k] = v
				}
				return result(t, rpc(t, srv, version, "resources/read", map[string]any{"uri": "dibs://wake-digest", "_meta": meta}), "socket offer")
			}
			text := func(r map[string]any) string { return r["contents"].([]any)[0].(map[string]any)["text"].(string) }
			first := send("original-failed-mail")
			idle()
			offer := read(nil)
			id, _ := offer["_meta"].(map[string]any)["com.dibs/socket_offer_id"].(string)
			if id == "" || !strings.Contains(text(offer), "original-failed-mail") {
				t.Fatal("setup: missing original offer", offer)
			}
			read(map[string]any{"com.dibs/socket_offer_id": id, "com.dibs/socket_written": false})
			toolCall(t, srv, "respond", map[string]any{"token": token, "msg_serial": first, "disposition": "answer", "body": "handled"})
			send("unrelated-new-mail")
			idle()
			retried := read(map[string]any{"com.dibs/socket_retry_offer": id})
			if text(retried) != "" {
				t.Fatalf("old failure retried unrelated new mail: %s", text(retried))
			}
			ordinary := read(nil)
			if !strings.Contains(text(ordinary), "unrelated-new-mail") {
				t.Fatal("refused retry spent new event's own offer", ordinary)
			}
		})
	}
}
