// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

func TestRespondChoiceRefusalShowsTheAnswerField(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return toolCallOn(t, srv, version, name, args)
			}
			sender := registerOn(t, srv, version, "ruling-author")
			recipient := registerOn(t, srv, version, "reviewer")
			sent := call("send", map[string]any{
				"token": sender, "to": "reviewer", "type": "question",
				"body": "Which wording?", "choices": []string{"Keep", "Revise"},
			})
			serial, ok := sent["msg_serial"].(float64)
			if !ok {
				t.Fatalf("question setup: %v", sent)
			}
			wrong := rpc(t, srv, version, "tools/call", map[string]any{
				"name": "respond", "arguments": map[string]any{
					"token": recipient, "msg_serial": serial, "disposition": "answer", "choice": "Revise",
				},
			})
			reply := asMap(wrong["error"])
			message, _ := reply["message"].(string)
			if reply["code"] != float64(-32602) || !strings.Contains(message, "selected label in body") {
				t.Errorf("unsupported choice field should name the answer field: %v", wrong)
			}
			answer := call("respond", map[string]any{
				"token": recipient, "msg_serial": serial, "disposition": "answer", "body": "Revise",
			})
			if answer["state"] != "answered" {
				t.Fatalf("answer using the stated field failed: %v", answer)
			}
			mail := call("read_mail", map[string]any{"token": sender, "msg_serial": serial, "include_body": true})
			if asMap(mail["message"])["response"] != "Revise" {
				t.Fatalf("chosen label was not preserved: %v", mail)
			}
		})
	}
}
