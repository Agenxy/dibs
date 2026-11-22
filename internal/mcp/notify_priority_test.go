// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

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
	// Inbox presents and consumes the complete FYI; read_mail must still
	// preserve priority on the recently consumed retained envelope.
	for _, name := range []string{"inbox", "read_mail"} {
		args := map[string]any{"token": recipient}
		if name == "read_mail" {
			args["msg_serial"] = serial
		}
		got := toolCall(t, srv, name, args)
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

func TestQuestionPriorityRefusalPointsToOmission(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			sender := registerOn(t, srv, version, "sender")
			registerOn(t, srv, version, "reviewer")
			args := map[string]any{
				"token": sender, "to": "reviewer", "type": "question",
				"body": "Can you check?", "priority": "high",
			}
			result := toolCallOn(t, srv, version, "send", args)
			hint, _ := result["hint"].(string)
			if result["code"] != "E_BAD_ARG" || !strings.Contains(hint, "notify") ||
				!strings.Contains(hint, "ordinary request") || !strings.Contains(hint, "Omit priority") {
				t.Errorf("priority refusal did not name the accepted types and corrective action: %v", result)
			}
			delete(args, "priority")
			accepted := toolCallOn(t, srv, version, "send", args)
			if accepted["msg_serial"] == nil {
				t.Fatalf("question without priority was refused: %v", accepted)
			}
		})
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
