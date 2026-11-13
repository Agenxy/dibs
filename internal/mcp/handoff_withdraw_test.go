// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// Enter through tools/call, including real hook presentation and encrypted
// restart. A pointer or a held hook must not invent a read receipt; a complete
// presentation must not leave a sender able to retract what was already read.
func TestUnreadHandoffWithdrawalThroughMCPAndRestart(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, replay := range []bool{false, true} {
			for _, presentation := range []string{"unread", "history", "sender-read", "pointer", "held-Stop", "inbox", "check_in", "read_mail", "Stop", "SubagentStop", "SessionStart", "UserPromptSubmit", "ack"} {
				t.Run(fmt.Sprintf("%s/replay=%t/%s", version, replay, presentation), func(t *testing.T) {
					dir := t.TempDir()
					srv, _, stop := questionWithdrawalServer(t, dir, version)
					defer func() { stop() }()
					lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
					worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker", "session_id": "handoff-withdraw-worker"})["token"]
					body := "Complete handoff evidence."
					if presentation == "pointer" {
						body = strings.Repeat("unread handoff evidence ", 1000)
					}
					n := questionWithdrawalOK(t, srv, version, "send", map[string]any{
						"token": lead, "to": "worker", "type": "handoff", "body": body,
					})["msg_serial"]
					switch presentation {
					case "history":
						questionWithdrawalOK(t, srv, version, "mail_history", map[string]any{"token": worker, "include_bodies": true})
					case "sender-read":
						questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": n, "include_body": true})
					case "inbox", "check_in", "read_mail":
						args := map[string]any{"token": worker}
						if presentation == "read_mail" {
							args["msg_serial"], args["include_body"] = n, true
						}
						questionWithdrawalOK(t, srv, version, presentation, args)
					case "ack":
						questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": worker, "msg_serial": n})
					case "pointer", "held-Stop", "Stop", "SubagentStop", "SessionStart", "UserPromptSubmit":
						event := presentation
						if presentation == "pointer" || presentation == "held-Stop" {
							event = "Stop"
						}
						args := map[string]any{"session_id": "handoff-withdraw-worker", "event": event}
						if presentation == "held-Stop" {
							args["stop_hook_active"] = true
						}
						shown := questionWithdrawalOK(t, srv, version, "hook_poll", args)
						text := fmt.Sprint(shown)
						switch presentation {
						case "pointer", "UserPromptSubmit":
							if !strings.Contains(text, "read_mail") || strings.Contains(text, body) {
								t.Fatalf("setup: expected a shortened handoff pointer: %v", shown)
							}
						case "held-Stop":
							if strings.Contains(text, body) {
								t.Fatalf("setup: held Stop presented handoff: %v", shown)
							}
						default:
							if !strings.Contains(text, body) {
								t.Fatalf("setup: hook did not show complete handoff: %v", shown)
							}
						}
					}
					if replay {
						stop()
						srv, _, stop = questionWithdrawalServer(t, dir, version)
					}
					result := questionWithdrawalCall(t, srv, version, "respond", map[string]any{
						"token": lead, "msg_serial": n, "disposition": "withdraw", "body": "superseded elsewhere",
					})
					unread := presentation == "unread" || presentation == "history" || presentation == "sender-read" || presentation == "pointer" || presentation == "held-Stop" || presentation == "UserPromptSubmit"
					if !unread {
						if result["__is_error"] != true || result["code"] != "E_MSG_FINAL" || !strings.Contains(fmt.Sprint(result["hint"]), "follow-up") {
							t.Fatalf("read handoff withdrawal must explain finality: %v", result)
						}
						mail := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": n})["message"].(map[string]any)
						wantState := "delivered"
						if presentation == "ack" {
							wantState = "acked"
							if mail["consumed"] != true {
								t.Fatalf("ack did not consume handoff: %v", mail)
							}
						} else if mail["consumed"] == true {
							t.Fatalf("reading handoff spent its explicit ack: %v", mail)
						}
						if mail["state"] != wantState {
							t.Fatalf("handoff receipt = %v, want %s", mail, wantState)
						}
						return
					}
					if result["__is_error"] == true || result["state"] != "withdrawn" {
						t.Fatalf("unread handoff withdrawal: %v", result)
					}
					stop()
					srv, _, stop = questionWithdrawalServer(t, dir, version)
					for _, event := range []string{"Stop", "UserPromptSubmit", "Stop"} {
						got := questionWithdrawalOK(t, srv, version, "hook_poll", map[string]any{"session_id": "handoff-withdraw-worker", "event": event})
						if got["decision"] == "block" || strings.Contains(fmt.Sprint(got), "withdrew") || strings.Contains(fmt.Sprint(got), "Complete handoff evidence") {
							t.Fatalf("withdrawn handoff still wakes its recipient: %v", got)
						}
					}
					mail := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": n})["message"].(map[string]any)
					if mail["state"] != "withdrawn" || mail["withdrawal_reason"] != "superseded elsewhere" {
						t.Fatalf("lost withdrawal after replay: %v", mail)
					}
				})
			}
		}
	}
}

func TestHandoffWithdrawalReplacementAndNoTransferThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, stop := questionWithdrawalServer(t, t.TempDir(), version)
			defer stop()
			lead := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "lead"})["token"]
			worker := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "worker"})["token"]
			stranger := questionWithdrawalOK(t, srv, version, "register", map[string]any{"name": "stranger"})["token"]
			send := func(token any) any {
				return questionWithdrawalOK(t, srv, version, "send", map[string]any{"token": token, "to": "worker", "type": "handoff", "body": "evidence only"})["msg_serial"]
			}
			n, replacement, foreign := send(lead), send(lead), send(stranger)
			for _, disposition := range []string{"approve", "accept"} {
				got := questionWithdrawalCall(t, srv, version, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": disposition})
				if got["__is_error"] != true || !strings.Contains(fmt.Sprint(got["hint"]), "ack") {
					t.Fatalf("handoff unexpectedly has an acceptance effect: %v", got)
				}
			}
			for _, args := range []map[string]any{
				{"token": stranger, "msg_serial": n, "disposition": "withdraw"},
				{"token": lead, "msg_serial": n, "disposition": "withdraw", "superseded_by": foreign},
			} {
				if got := questionWithdrawalCall(t, srv, version, "respond", args); got["__is_error"] != true {
					t.Fatalf("foreign withdrawal/replacement accepted: %v", got)
				}
			}
			questionWithdrawalOK(t, srv, version, "respond", map[string]any{"token": lead, "msg_serial": n, "disposition": "withdraw", "superseded_by": replacement})
			for _, disposition := range []string{"approve", "accept"} {
				if got := questionWithdrawalCall(t, srv, version, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": disposition}); got["__is_error"] != true {
					t.Fatalf("withdrawn handoff could be accepted: %v", got)
				}
			}
			// Reading/acking a withdrawn envelope cannot turn it back into an
			// accepted handoff or execute its replacement.
			questionWithdrawalOK(t, srv, version, "ack", map[string]any{"token": worker, "msg_serial": n})
			mail := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": worker, "msg_serial": n})["message"].(map[string]any)
			if mail["state"] != "withdrawn" || mail["superseded_by"] != replacement {
				t.Fatalf("withdrawn handoff changed: %v", mail)
			}
			other := questionWithdrawalOK(t, srv, version, "read_mail", map[string]any{"token": lead, "msg_serial": replacement})["message"].(map[string]any)
			if other["state"] != "pending" {
				t.Fatalf("withdrawal acted on replacement: %v", other)
			}
		})
	}
}
