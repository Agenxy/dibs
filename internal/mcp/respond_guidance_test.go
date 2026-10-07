// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// The real MCP/engine door must disclose the obligation and executable repair
// call. A helper-only string assertion could pass while ingress loses the hint.
func TestRespondGuidanceThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, scenario := range []string{
			"approve", "question-approve", "question-done", "question-progress", "question-queue", "milestone",
		} {
			t.Run(version+"/"+scenario, func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				_, lead := aliasRegister(t, srv, "lead", "guidance-lead")
				_, worker := aliasRegister(t, srv, "worker", "guidance-worker")
				kind := "request"
				if strings.HasPrefix(scenario, "question-") {
					kind = "question"
				}
				sendArgs := map[string]any{
					"token": lead, "to": "worker", "type": kind, "body": "do work or answer",
				}
				if kind == "request" {
					sendArgs["milestones"] = []string{"proof", "delivery"}
				}
				sent := toolCallOn(t, srv, version, "send", sendArgs)
				serial, ok := sent["msg_serial"].(float64)
				if !ok || serial == 0 {
					t.Fatalf("setup send: %v", sent)
				}
				args := map[string]any{"token": worker, "msg_serial": serial, "disposition": "approve"}
				if strings.HasPrefix(scenario, "question-") {
					args["disposition"] = strings.TrimPrefix(scenario, "question-")
				}
				if scenario == "milestone" {
					approved := toolCallOn(t, srv, version, "respond", args)
					if approved["state"] != "approved" {
						t.Fatalf("setup approval: %v", approved)
					}
					progress := toolCallOn(t, srv, version, "respond", map[string]any{
						"token": worker, "msg_serial": serial, "disposition": "progress", "milestone": 1,
						"body": "proof ready",
					})
					if progress["ok"] != true {
						t.Fatalf("setup progress: %v", progress)
					}
					args["token"], args["disposition"], args["milestone"] = lead, "accept", 2
				}
				r := toolCallOn(t, srv, version, "respond", args)
				call := fmt.Sprintf("respond(msg_serial:%.0f, disposition:\"", serial)
				switch scenario {
				case "approve":
					if r["state"] != "approved" || !mentions(r, "I'll do it") || !mentions(r, "owe") ||
						!strings.Contains(fmt.Sprint(r), call+"done\"") || !mentions(r, "type:question") {
						t.Fatalf("approval conceals work obligation or permission alternative: %v", r)
					}
				case "question-approve", "question-done", "question-progress", "question-queue":
					if r["code"] != "E_BAD_DISPOSITION" || !strings.Contains(fmt.Sprint(r["hint"]), call+"answer\"") {
						t.Fatalf("question refusal lacks exact corrective call: %v", r)
					}
				case "milestone":
					if r["code"] != "E_MILESTONE_UNREPORTED" ||
						!strings.Contains(fmt.Sprint(r["hint"]), call+"accept\", milestone:1)") {
						t.Fatalf("unreported milestone lacks reported-step call: %v", r)
					}
				}
			})
		}
	}
}

func TestMissingTokenHintsNonceRecoveryThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := aliasReplayServer(t, t.TempDir())
			id, token := aliasRegister(t, srv, "recover", "guidance-saved-nonce")
			_, sender := aliasRegister(t, srv, "sender", "guidance-sender")
			mail := toolCallOn(t, srv, version, "send", map[string]any{
				"token": sender, "to": id, "type": "notify", "body": "mail must survive token recovery",
			})
			if mail["msg_serial"] == nil {
				t.Fatalf("setup mail: %v", mail)
			}
			out := rpc(t, srv, version, "tools/call", map[string]any{
				"name": "check_in", "arguments": map[string]any{},
			})
			err, ok := out["error"].(map[string]any)
			if !ok || err["code"] != float64(-32602) || !mentions(err, "register(nonce:") ||
				!mentions(err, "saved nonce") || !mentions(err, "returned token") {
				t.Fatalf("missing token cannot recover identity: %v", out)
			}
			r := toolCallOn(t, srv, version, "register", map[string]any{"nonce": "guidance-saved-nonce"})
			if r["agent_id"] != id || r["token"] != token {
				t.Fatalf("recovery made a sibling: %v", r)
			}
			checkpoint := toolCallOn(t, srv, version, "check_in", map[string]any{"token": r["token"]})
			if !mentions(checkpoint, "mail must survive token recovery") {
				t.Fatalf("recovery lost mail: %v", checkpoint)
			}
		})
	}
}
