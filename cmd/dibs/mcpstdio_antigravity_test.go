// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"testing"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/mcp"
)

func TestAntigravitySharedStdioUsesOnlyPerCallConversation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_AGENT_NONCE", "shared-config")
	writeLegacyRecoveryCache(t, dir, map[string]string{dir + "\x00worker": "unscoped-credential"})
	prev := antigravityStdio
	antigravityStdio = func() bool { return true }
	t.Cleanup(func() { antigravityStdio = prev })
	makeCall := func(conversation string) map[string]any {
		t.Helper()
		in, err := json.Marshal(map[string]any{
			"method": "tools/call",
			"params": map[string]any{
				"name": "register",
				"arguments": map[string]any{
					"name": "worker", "cwd": dir, "session_id": "guess", "pid": 123,
				},
				"_meta": map[string]any{
					antigravity.ConversationMeta: conversation, "threadId": "another-thread",
					mcp.SessionMetaKey: "shared-process",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Params map[string]any }
		if err := json.Unmarshal(enrichRegister(in), &out); err != nil || !antigravityBridgeMarked(out.Params) {
			t.Fatal("real register path did not stamp the app bridge")
		}
		args := out.Params["arguments"].(map[string]any)
		meta := out.Params["_meta"].(map[string]any)
		if meta["threadId"] != nil || meta[mcp.SessionMetaKey] != "" ||
			args["session_id"] != nil || args["pid"] != nil || args["recovery_nonces"] != nil {
			t.Fatal("shared bridge forwarded process identity or legacy recovery")
		}
		return args
	}

	const first = "01a10520-0000-7000-8000-000000000001"
	const second = "01a10520-0000-7000-8000-000000000002"
	a, b, again := makeCall(first)["nonce"], makeCall(second)["nonce"], makeCall(first)["nonce"]
	if a == nil || b == nil || a == b || a != again || a == "shared-config" || a == "unscoped-credential" {
		t.Fatal("conversation did not exclusively reuse its own credential")
	}
	if missing := makeCall("")["nonce"]; missing != nil {
		t.Fatal("call without app metadata received a cached credential")
	}
}
