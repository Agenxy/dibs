// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// Production hooks after real withdrawal and explicit acknowledgement. A
// terminal envelope and its recipient's outstanding receipt are different:
// unacknowledged withdrawals still have to be shown, acknowledged ones don't.
func TestConsumedWithdrawalLeavesNoHookOrOwedUnits(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"question", "request"} {
			for _, clear := range []string{"ack", "read_mail"} {
				t.Run(version+"/"+kind+"/"+clear, func(t *testing.T) {
					dir := t.TempDir()
					srv, _, stop := questionWithdrawalServer(t, dir, version)
					lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
					worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker", "session_id": "withdraw-worker"})["token"]
					questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": lead})
					questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": worker})
					id := questionWithdrawalOK(t, srv, version, "send", map[string]any{
						"token": lead, "to": "worker", "type": kind, "body": "work that will be withdrawn",
					})["msg_serial"]
					if kind == "request" {
						questionWithdrawalOK(t, srv, version, "respond", map[string]any{"token": worker, "msg_serial": id, "disposition": "approve"})
					}
					withdrawn := questionWithdrawalOK(t, srv, version, "respond", map[string]any{
						"token": lead, "msg_serial": id, "disposition": "withdraw", "body": "withdrawn-reason-marker",
					})
					if withdrawn["state"] != "withdrawn" {
						t.Fatalf("setup: withdrawal not applied: %v", withdrawn)
					}
					before := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{"session_id": "withdraw-worker", "event": "UserPromptSubmit"})
					if !strings.Contains(fmt.Sprint(before), "1 outstanding update units") {
						t.Fatalf("setup: live withdrawal receipt was not outstanding: %v", before)
					}
					cleared := questionWithdrawalOK(t, srv, version, clear, map[string]any{"token": worker, "msg_serial": id})
					consumed := cleared["consumed"]
					if clear == "read_mail" {
						consumed = cleared["message"].(map[string]any)["consumed"]
					}
					if consumed != true {
						t.Errorf("%s did not consume the recipient's withdrawn %s receipt: %v", clear, kind, cleared)
					}
					assertQuiet := func(stage string) {
						t.Helper()
						for _, event := range []string{"Stop", "UserPromptSubmit", "Stop"} {
							out := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{"session_id": "withdraw-worker", "event": event})
							text := fmt.Sprint(out)
							if out["decision"] == "block" || strings.Contains(text, "withdrew ") || strings.Contains(text, "outstanding update units") {
								t.Errorf("%s %s counts consumed withdrawal as outstanding: %v", stage, event, out)
							}
						}
					}
					assertQuiet("live")
					stop()
					srv, _, stop = questionWithdrawalServer(t, dir, version)
					defer stop()
					assertQuiet("encrypted restart")
					checkpoint := questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": worker})
					if len(checkpoint["owes"].([]any)) != 0 || len(checkpoint["owed_work"].([]any)) != 0 || strings.Contains(fmt.Sprint(checkpoint["agent_updates"]), "withdrew ") {
						t.Fatalf("consumed withdrawal remains in checkpoint accounting: %v", checkpoint)
					}
				})
			}
		}
	}
}

func TestWithdrawalAckKeepsOtherUnreadReceipts(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"question", "request"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				srv, _, _ := questionWithdrawalServer(t, t.TempDir(), version)
				lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
				worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker", "session_id": "withdraw-worker"})["token"]
				questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": lead})
				questionWithdrawalOK(t, srv, version, "check_in", map[string]any{"token": worker})
				var ids []any
				for i := range 2 {
					id := questionWithdrawalOK(t, srv, version, "send", map[string]any{"token": lead, "to": "worker", "type": kind, "body": "withdrawal original body"})["msg_serial"]
					ids = append(ids, id)
					questionWithdrawalOK(t, srv, version, "respond", map[string]any{"token": lead, "msg_serial": id, "disposition": "withdraw", "body": fmt.Sprintf("independent-receipt-%d", i)})
					// The sender's acknowledgement cannot consume the recipient's
					// independent receipt, even though the same envelope is terminal.
					questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": lead, "msg_serial": id})
				}
				before := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{"session_id": "withdraw-worker", "event": "UserPromptSubmit"})
				if !strings.Contains(fmt.Sprint(before), "2 outstanding update units") {
					t.Fatalf("sender ack suppressed recipient receipts: %v", before)
				}
				questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": worker, "msg_serial": ids[0]})
				after := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{"session_id": "withdraw-worker", "event": "Stop"})
				text := fmt.Sprint(after)
				if after["decision"] != "block" || !strings.Contains(text, "1 outstanding update units") || !strings.Contains(text, "independent-receipt-1") || strings.Contains(text, "independent-receipt-0") {
					t.Fatalf("ack of one withdrawal lost another or retained the cleared one: %v", after)
				}
				kept := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": ids[0]})["message"].(map[string]any)
				if kept["body"] != "withdrawal original body" || kept["withdrawal_reason"] != "independent-receipt-0" {
					t.Fatalf("recipient ack removed sender's retained envelope: %v", kept)
				}
			})
		}
	}
}
