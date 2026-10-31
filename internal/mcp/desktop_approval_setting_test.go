// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"testing"
)

// Enter at tools/call, without granting the member an admin role or setting
// engine state by hand. Both protocol paths must allow tightening this one
// control and refuse weakening it with an agent token.
func TestDesktopApprovalSettingThroughBothMCPProtocols(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				out := rpc(t, srv, version, "tools/call", map[string]any{"name": name, "arguments": args})
				result, ok := out["result"].(map[string]any)
				if !ok {
					t.Fatalf("%s setup: %v", name, out)
				}
				content, ok := result["content"].([]any)
				if !ok || len(content) == 0 {
					t.Fatalf("%s result shape: %v", name, result)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &payload); err != nil {
					t.Fatal(err)
				}
				return payload
			}
			registered := call("register", map[string]any{"name": "approval-member", "nonce": "approval-member-nonce"})
			token, ok := registered["token"].(string)
			if !ok || token == "" {
				t.Fatalf("registration setup: %v", registered)
			}
			const key = "human.confirm_privileged_desktop"
			enabled := call("settings", map[string]any{"token": token, "setting": key, "value": "true"})
			if enabled["setting"] != key || enabled["value"] != "true" || enabled["saved"] != true {
				t.Fatalf("ordinary authenticated agent cannot enable the durable approval check: %v", enabled)
			}
			disabled := call("settings", map[string]any{"token": token, "setting": key, "value": "false"})
			if disabled["code"] != "E_HUMAN_SETTING" {
				t.Fatalf("an agent disabled the human control, or got no corrective: %v", disabled)
			}
			listed := call("settings", map[string]any{"token": token})
			rows, ok := listed["settings"].([]any)
			if !ok {
				t.Fatalf("settings listing: %v", listed)
			}
			for _, item := range rows {
				row := item.(map[string]any)
				if row["setting"] == key {
					if row["value"] != "true" || row["set_by"] == "" || row["set_at"] == nil {
						t.Fatalf("refused disable changed state or lost provenance: %v", row)
					}
					return
				}
			}
			t.Fatal("enabled setting is absent from its own listing")
		})
	}
}
