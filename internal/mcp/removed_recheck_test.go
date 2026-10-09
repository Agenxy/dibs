// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemovedRecheckIsAbsentAndRefusedOnBothProtocols(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			list := result(t, rpc(t, srv, version, "tools/list", map[string]any{}), "tools/list")
			for _, item := range list["tools"].([]any) {
				tool := item.(map[string]any)
				if tool["name"] == "declare" {
					props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
					if _, exists := props["recheck_after"]; exists {
						t.Error("removed timer remains advertised")
					}
				}
			}
			token := toolCall(t, srv, "register", map[string]any{"name": "worker"})["token"]
			toolCall(t, srv, "check_in", map[string]any{"token": token})
			for _, value := range []any{"20m", "", nil, 20} {
				out := result(t, rpc(t, srv, version, "tools/call", map[string]any{
					"name": "declare", "arguments": map[string]any{"token": token, "text": "review", "waiting": "ci", "recheck_after": value},
				}), "declare")
				text := out["content"].([]any)[0].(map[string]any)["text"].(string)
				var refusal struct {
					Hint string `json:"hint"`
				}
				if err := json.Unmarshal([]byte(text), &refusal); err != nil {
					t.Fatal(err)
				}
				expected := "recheck_after was removed: Dibs no longer runs timers; set your own (a shell sleep works) and declare waiting without it."
				if refusal.Hint != expected {
					t.Errorf("incorrect corrective hint: %q", refusal.Hint)
				}
				if out["isError"] != true || !strings.Contains(text, "recheck_after was removed") {
					t.Errorf("removed value %v was not refused with corrective hint: %s", value, text)
				}
			}
			board := toolCall(t, srv, "check_in", map[string]any{"token": token})
			encoded, err := json.Marshal(board)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"text":"review"`) {
				t.Fatal("refusal still changed declarations")
			}
			good := toolCall(t, srv, "declare", map[string]any{"token": token, "text": "valid wait", "waiting": "ci"})
			if good["__is_error"] == true {
				t.Fatalf("ordinary waiting refused: %v", good)
			}
		})
	}
}
