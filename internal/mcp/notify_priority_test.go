// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import "testing"

func TestPriorityNotifyRoundTripsThroughMCP(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})["token"].(string)
	recipient := toolCall(t, srv, "register", map[string]any{"name": "recipient"})["token"].(string)
	sent := toolCall(t, srv, "send", map[string]any{
		"token": sender, "to": "recipient", "type": "notify", "body": "operational alert", "priority": "high",
	})
	if sent["__is_error"] == true {
		t.Fatalf("priority notify refused: %v", sent)
	}
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("priority notify was not stored: %v", sent)
	}
	for name, got := range map[string]map[string]any{
		"read_mail": toolCall(t, srv, "read_mail", map[string]any{"token": recipient, "msg_serial": serial}),
		"inbox":     toolCall(t, srv, "inbox", map[string]any{"token": recipient}),
	} {
		var message map[string]any
		if name == "read_mail" {
			message, _ = got["message"].(map[string]any)
		} else if items, ok := got["messages"].([]any); ok && len(items) == 1 {
			message, _ = items[0].(map[string]any)
		}
		if message == nil || message["request_priority"] != "high" || message["type"] != "notify" {
			t.Fatalf("%s lost notify priority: %v", name, got)
		}
	}
	for _, bad := range []map[string]any{
		{"type": "notify", "priority": "immediate"},
		{"type": "question", "priority": "high"},
	} {
		bad["token"], bad["to"], bad["body"] = sender, "recipient", "invalid priority control"
		if result := toolCall(t, srv, "send", bad); result["code"] != "E_BAD_ARG" {
			t.Fatalf("invalid priority accepted: %v", result)
		}
	}
}

func TestPriorityNotifySurvivesLedgerReplay(t *testing.T) {
	dir := t.TempDir()
	srv, _, stop := restartableQueueServer(t, dir)
	sender := toolCall(t, srv, "register", map[string]any{
		"name": "sender", "nonce": "notify-sender",
	})["token"].(string)
	recipient := toolCall(t, srv, "register", map[string]any{
		"name": "recipient", "nonce": "notify-recipient",
	})["token"].(string)
	sent := toolCall(t, srv, "send", map[string]any{
		"token": sender, "to": "recipient", "type": "notify", "body": "durable alert", "priority": "urgent",
	})
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("send setup: %v", sent)
	}
	stop()
	srv, _, _ = restartableQueueServer(t, dir)
	read := toolCall(t, srv, "read_mail", map[string]any{"token": recipient, "msg_serial": serial})
	message, _ := read["message"].(map[string]any)
	if message == nil || message["request_priority"] != "urgent" || message["body"] != "durable alert" {
		t.Fatalf("replay lost notify priority: %v", read)
	}
}
