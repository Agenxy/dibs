// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

func TestHandoffWithdrawalCanNameReplacementToAnotherRecipient(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			sender := registerOn(t, srv, version, "sender")
			originalRecipient := registerOn(t, srv, version, "original-recipient")
			registerOn(t, srv, version, "new-recipient")
			send := func(token, to string) float64 {
				t.Helper()
				result := toolCallOn(t, srv, version, "send", map[string]any{
					"token": token, "to": to, "type": "handoff", "body": "review context",
				})
				serial, ok := result["msg_serial"].(float64)
				if !ok {
					t.Fatalf("handoff setup: %v", result)
				}
				return serial
			}

			original := send(sender, "original-recipient")
			replacement := send(sender, "new-recipient")
			foreign := send(originalRecipient, "new-recipient")
			withdraw := func(next float64) map[string]any {
				return toolCallOn(t, srv, version, "respond", map[string]any{
					"token": sender, "msg_serial": original, "disposition": "withdraw", "superseded_by": next,
				})
			}
			refused := withdraw(foreign)
			hint, _ := refused["hint"].(string)
			if refused["code"] != "E_NO_MESSAGE" || !strings.Contains(hint, "The recipient may differ") ||
				!strings.Contains(hint, "read_mail") || !strings.Contains(hint, "YOU sent") {
				t.Errorf("refusal should teach replacement eligibility without exposing foreign mail: %v", refused)
			}

			accepted := withdraw(replacement)
			if accepted["state"] != "withdrawn" || accepted["superseded_by"] != replacement {
				t.Fatalf("a handoff to another recipient should qualify: %v", accepted)
			}
			mail := toolCallOn(t, srv, version, "read_mail", map[string]any{
				"token": sender, "msg_serial": replacement, "include_body": true,
			})
			next := asMap(mail["message"])
			if next["state"] != "pending" || next["to"] != "new-recipient" {
				t.Fatalf("withdrawal changed the replacement: %v", mail)
			}
			unreadable := toolCallOn(t, srv, version, "read_mail", map[string]any{
				"token": originalRecipient, "msg_serial": replacement,
			})
			if unreadable["code"] != "E_NOT_YOUR_MESSAGE" || mentions(unreadable, "review context") {
				t.Fatalf("withdrawal broadened replacement access: %v", unreadable)
			}
		})
	}
}
