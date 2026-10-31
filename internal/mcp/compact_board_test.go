// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

// The actual MCP entry must carry the engine's machine evidence and name
// history into the private UI payload. A hand-built trimBoard input would miss
// the source of either field and let disconnected rendering appear complete.
func TestCompactBoardGetsHostEvidenceAndFormerNamesThroughMCP(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	friendly := hostdisplay.Name()
	local := toolCall(t, srv, "register", map[string]any{
		"name": "local", "nonce": "compact-local", "host": friendly,
	})
	token, _ := local["token"].(string)
	if token == "" {
		t.Fatalf("local registration setup: %v", local)
	}
	toolCall(t, srv, "check_in", map[string]any{"token": token})
	for _, name := range []string{"former-label", "current-label"} {
		updated := toolCall(t, srv, "update", map[string]any{"token": token, "name": name})
		if updated["error"] != nil {
			t.Fatalf("rename setup: %v", updated)
		}
	}
	remote := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{
		"name": "register", "arguments": map[string]any{
			"name": "remote", "nonce": "compact-remote", "host": friendly,
		}, "_meta": map[string]any{HostMetaKey: "another-machine"},
	})
	if asMap(remote["result"])["isError"] == true {
		t.Fatalf("remote setup refused: %v", remote)
	}
	response := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{
		"name": "board", "arguments": map[string]any{"token": token},
	})
	result := asMap(response["result"])
	payload := asMap(asMap(result["_meta"])["com.dibs/panel"])
	rows := asMaps(asMap(payload["board"])["agents"])
	seen := map[string]bool{}
	for _, row := range rows {
		id, _ := row["id"].(string)
		if id != "local" && id != "remote" {
			continue
		}
		seen[id] = true
		if row["host"] != friendly {
			t.Fatalf("setup: expected equal display labels on different machines: %v", row)
		}
		if (row["local_host_label"] == true) != (id == "local") {
			t.Errorf("machine evidence was lost or guessed from its display label: %v", row)
		}
		if id == "local" {
			aliases, _ := json.Marshal(row["name_aliases"])
			if !strings.Contains(string(aliases), "former-label") {
				t.Errorf("the former name cannot be searched: %s", aliases)
			}
		}
	}
	if !seen["local"] || !seen["remote"] {
		t.Fatalf("setup: actual UI payload lost the registered agents: %v", payload)
	}
}
