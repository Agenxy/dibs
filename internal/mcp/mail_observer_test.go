// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Enter through the actual board tool, including the private detail fetch the
// panel uses. A human seeing mail is not the recipient model seeing it.
func TestBoardReadsLeaveFYIForModelInbox(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			eng := srv.Config.Handler.(*Server).eng
			reader := toolCall(t, srv, "register", map[string]any{"name": "reader"})
			sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})
			const body = "FYI still owed to the model after two human panel reads"
			sent := waitingCall(t, srv, version, "send", map[string]any{
				"token": sender["token"], "to": "reader", "type": "notify", "body": body,
			})
			id := uint64(sent["msg_serial"].(float64))
			question := waitingCall(t, srv, version, "send", map[string]any{
				"token": reader["token"], "to": "sender", "type": "question", "body": "Unseen outcome?",
			})
			qid := uint64(question["msg_serial"].(float64))
			waitingCall(t, srv, version, "respond", map[string]any{
				"token": sender["token"], "msg_serial": qid, "disposition": "answer", "body": "Outcome still unread by model",
			})
			for _, detail := range []bool{false, true} {
				out := rpc(t, srv, version, "tools/call", map[string]any{
					"name": "board", "arguments": map[string]any{"token": reader["token"], "view": "mail", "detail": detail},
				})
				result, ok := out["result"].(map[string]any)
				if !ok || result["isError"] == true {
					t.Fatalf("board setup failed: %v", out)
				}
				if !detail {
					model, _ := json.Marshal(map[string]any{"content": result["content"], "structuredContent": result["structuredContent"]})
					if strings.Contains(string(model), body) {
						t.Fatal("human-only board body leaked to model")
					}
				}
				m := mailboxReceipt(t, eng, id)
				if m.Consumed || m.AckedAt != 0 || m.State != core.MsgStatePending || m.DeliveredAt != 0 {
					t.Fatalf("human observer changed model mail receipt: %+v", m)
				}
				if mailboxReceipt(t, eng, qid).OutcomeReadAt != 0 {
					t.Fatal("human observer consumed model's unseen outcome")
				}
			}
			page := waitingCall(t, srv, version, "inbox", map[string]any{"token": reader["token"]})
			items, ok := page["inbox"].([]any)
			if !ok || len(items) != 1 || items[0].(map[string]any)["body"] != body {
				t.Fatalf("board then board lost FYI before model inbox: %v", page)
			}
			if !mailboxReceipt(t, eng, id).Consumed {
				t.Fatal("full model inbox presentation did not consume FYI")
			}
			out := rpc(t, srv, version, "tools/call", map[string]any{
				"name": "board", "arguments": map[string]any{"token": reader["token"], "detail": true, "view": "mail"},
			})
			result := out["result"].(map[string]any)
			meta := result["_meta"].(map[string]any)["com.dibs/panel"].(map[string]any)
			empty, ok := meta["inbox"].([]any)
			if !ok || len(empty) != 0 {
				t.Fatalf("empty observer mailbox absent from private refresh carrier: inbox=%v", meta["inbox"])
			}
		})
	}
}
