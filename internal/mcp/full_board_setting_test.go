// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestFullBoardPolicyThroughBothMCPProtocols(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				out := rpc(t, srv, version, "tools/call", map[string]any{"name": name, "arguments": args})
				result, ok := out["result"].(map[string]any)
				if !ok {
					t.Fatalf("%s response: %v", name, out)
				}
				content := result["content"].([]any)[0].(map[string]any)["text"].(string)
				var payload map[string]any
				if err := json.Unmarshal([]byte(content), &payload); err != nil {
					t.Fatal(err)
				}
				return payload
			}
			registered := call("register", map[string]any{"name": "policy-lead", "nonce": "policy-lead-nonce"})
			token, ok := registered["token"].(string)
			if !ok || token == "" {
				t.Fatalf("registration setup: %v", registered)
			}
			change := func(value string) map[string]any {
				return call("settings", map[string]any{"token": token, "setting": core.FullBoardSettingKey, "value": value})
			}
			if got := change(core.FullBoardRefuse); got["code"] != "E_NOT_COORDINATOR" {
				t.Fatalf("member changed policy: %v", got)
			}
			if _, err := eng.GrantRole(context.Background(), "policy-lead", core.RoleCoordinator); err != nil {
				t.Fatal("grant setup:", err)
			}
			if got := change("invalid"); got["code"] != "E_BAD_SETTING" {
				t.Fatalf("invalid policy: %v", got)
			}
			for _, value := range []string{core.FullBoardRefuse, core.FullBoardDisplaceIdle} {
				got := change(value)
				if got["value"] != value || got["saved"] != true || got["by"] != "policy-lead" {
					t.Fatalf("coordinator change: %v", got)
				}
			}
			listed := call("settings", map[string]any{"token": token})
			for _, item := range listed["settings"].([]any) {
				row := item.(map[string]any)
				if row["setting"] == core.FullBoardSettingKey {
					if row["value"] != core.FullBoardDisplaceIdle || row["set_by"] != "policy-lead" || row["set_at"] == nil {
						t.Fatalf("provenance: %v", row)
					}
					return
				}
			}
			t.Fatal("policy missing from settings")
		})
	}
}
