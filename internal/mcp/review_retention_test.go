// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import "testing"

func TestFlaggedDoneCorrectionThroughMCP(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r := toolCall(t, srv, name, args)
		if r["__is_error"] == true {
			t.Fatalf("setup %s: %v", name, r)
		}
		return r
	}
	lead := call("register", map[string]any{"name": "lead", "nonce": "retention-mcp-lead"})["token"]
	worker := call("register", map[string]any{"name": "worker", "nonce": "retention-mcp-worker"})["token"]
	parent := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "proof", "milestones": []string{"proof"}})["msg_serial"]
	call("respond", map[string]any{"token": worker, "msg_serial": parent, "disposition": "approve"})
	call("respond", map[string]any{"token": worker, "msg_serial": parent, "disposition": "done", "body": "delivered", "deliverable": "original"})
	progress := map[string]any{"token": worker, "msg_serial": parent, "disposition": "progress", "milestone": 1, "body": "corrected", "deliverable": "correction"}
	if toolCall(t, srv, "respond", progress)["__is_error"] != true {
		t.Fatal("unflagged done work accepted progress")
	}
	call("respond", map[string]any{"token": lead, "msg_serial": parent, "disposition": "flag", "body": "whole work needs correction"})
	call("respond", progress)
	call("respond", map[string]any{"token": lead, "msg_serial": parent, "disposition": "accept"})
	if toolCall(t, srv, "respond", progress)["__is_error"] != true {
		t.Fatal("resolved done work accepted more progress")
	}
	read := call("read_mail", map[string]any{"token": lead, "msg_serial": parent})
	m := read["message"].(map[string]any)
	if m["state"] != "done" || m["response"] != "done: delivered" || m["deliverable"] != "original" || m["retain_until"] == nil {
		t.Fatalf("correction lost the original completion or retention: %v", m)
	}
	entries := m["progress"].([]any)
	if len(entries) != 3 || entries[1].(map[string]any)["artifact"] != "correction" || entries[2].(map[string]any)["review"] != "accepted" {
		t.Fatalf("correction or whole-work acceptance missing: %v", entries)
	}
}
