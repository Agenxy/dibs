// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

func TestPrivateRulingRefusalNamesWhoCanShareIt(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			sender := registerOn(t, srv, version, "decision-requester")
			reviewer := registerOn(t, srv, version, "reviewer")
			assignee := registerOn(t, srv, version, "assignee")
			sent := toolCallOn(t, srv, version, "send", map[string]any{
				"token": sender, "to": "reviewer", "type": "question", "body": "private prompt",
			})
			serial, ok := sent["msg_serial"].(float64)
			if !ok {
				t.Fatalf("question setup failed: %v", sent)
			}
			answer := toolCallOn(t, srv, version, "respond", map[string]any{
				"token": reviewer, "msg_serial": serial, "disposition": "answer", "body": "private ruling",
			})
			if answer["state"] != "answered" {
				t.Fatalf("ruling setup failed: %v", answer)
			}
			args := map[string]any{"token": assignee, "msg_serial": serial, "include_body": true}
			denied := toolCallOn(t, srv, version, "read_mail", args)
			hint, _ := denied["hint"].(string)
			if denied["code"] != "E_NOT_YOUR_MESSAGE" || !strings.Contains(hint, "ask decision-requester to share") {
				t.Errorf("unreadable ruling needs a sharing hint naming the original sender: %v", denied)
			}
			if mentions(denied, "private prompt") || mentions(denied, "private ruling") {
				t.Fatalf("the refusal disclosed the conversation: %v", denied)
			}
			args["token"] = sender
			own := toolCallOn(t, srv, version, "read_mail", args)
			if asMap(own["message"])["response"] != "private ruling" {
				t.Fatalf("the named sender must be able to read the ruling: %v", own)
			}
		})
	}
}
