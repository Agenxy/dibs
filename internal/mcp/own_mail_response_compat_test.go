// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"testing"
)

// A compact sender read keeps the documented outcome fields in message.
// The original body is the part omitted, not the answer being collected.
func TestCompactOwnMailPreservesResponseAndDeliverable(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"question", "request"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				srv, eng, _ := aliasReplayServer(t, t.TempDir())
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					return waitingCall(t, srv, version, name, args)
				}
				sender := call("register", map[string]any{"name": "sender", "nonce": "outcome-compat-sender"})["token"]
				worker := call("register", map[string]any{"name": "worker", "nonce": "outcome-compat-worker"})["token"]
				call("check_in", map[string]any{"token": sender})
				call("check_in", map[string]any{"token": worker})
				n := call("send", map[string]any{"token": sender, "to": "worker", "type": kind, "body": "original task"})["msg_serial"].(float64)
				args := map[string]any{"token": worker, "msg_serial": n, "disposition": "answer", "body": "exact outcome\n    preserved indentation"}
				wantState := "answered"
				if kind == "request" {
					args["disposition"], args["deliverable"] = "done", "https://example.test/completed-work"
					wantState = "done"
				}
				call("respond", args)
				before := mailboxReceipt(t, eng, uint64(n))
				if !before.HasUnreadOutcome() {
					t.Fatal("setup: no unread result")
				}
				got := call("read_mail", map[string]any{"token": sender, "msg_serial": n})
				message := got["message"].(map[string]any)
				if message["response"] != before.Response || message["state"] != wantState {
					t.Fatalf("documented response/state lost in compact sender read: %v", got)
				}
				if _, present := message["body"]; present || message["body_omitted"] != true {
					t.Fatalf("compact response repeated the original task: %v", message)
				}
				if message["responded_serial"] != float64(before.RespondedAt) || message["terminal_at"] == nil {
					t.Fatalf("outcome verdict receipts lost: %v", message)
				}
				if kind == "request" && message["deliverable"] != before.Deliverable {
					t.Fatalf("completed request lost its deliverable: %v", message)
				}
				updates, ok := got["updates"].([]any)
				if !ok || len(updates) != 1 {
					t.Fatalf("outcome unit missing: %v", got)
				}
				if _, repeated := updates[0].(map[string]any)["body"]; repeated {
					t.Fatalf("verdict response repeated in updates: %v", got)
				}
			})
		}
	}
}
