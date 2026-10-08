// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/netip"
	"os/exec"
	"path/filepath"
	"testing"
)

// Actual command, invitation gate and task handler. No fake handles, cached
// handshake capability, background polling or tasks-to-progress translation.
func TestGuestBridgeTasksRemainPerRequestAndPullOnly(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build actual bridge: %v %s", err, out)
	}
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, key, _ := nativeGuestFixture(t, config.Certificates[0], "::1")
	file := guestCommandRecipe(t, endpoint, key, pem, pin)
	call := func(method string, params map[string]any) map[string]any {
		t.Helper()
		request, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": "task-fixture", "method": method, "params": params,
		})
		if err != nil {
			t.Fatal(err)
		}
		out := guestCommandOutput(t, bin, file, request)
		var reply map[string]any
		if err := json.Unmarshal(out, &reply); err != nil {
			t.Fatalf("command did not forward one JSON-RPC reply: %v", err)
		}
		return reply
	}
	meta := func(token string, tasks bool) map[string]any {
		m := map[string]any{
			"io.modelcontextprotocol/protocolVersion": "2026-07-28", "com.dibs/token": token,
		}
		if tasks {
			m["io.modelcontextprotocol/clientCapabilities"] = map[string]any{
				"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}},
			}
		}
		return m
	}
	registered := call("tools/call", map[string]any{
		"name": "register", "arguments": map[string]any{"name": "native-guest", "kind": "persistent"},
		"_meta": meta("", false),
	})
	identity := guestToolJSON(t, registered)
	token, ok := identity["token"].(string)
	if !ok || token == "" {
		t.Fatal("register setup failed to issue a token")
	}
	_ = guestToolJSON(t, call("tools/call", map[string]any{
		"name": "check_in", "arguments": map[string]any{"token": token}, "_meta": meta(token, false),
	}))
	sent := call("tools/call", map[string]any{
		"name": "send", "arguments": map[string]any{
			"token": token, "to": "human", "type": "request", "body": "fixture request", "track": true,
		}, "_meta": meta(token, true),
	})
	result, ok := sent["result"].(map[string]any)
	if !ok || result["resultType"] != "task" || result["status"] != "working" {
		t.Fatalf("real invitation send did not create an MCP task: %v", sent)
	}
	id, ok := result["taskId"].(string)
	if !ok || id == "" {
		t.Fatal("server did not issue a task handle")
	}
	polled := call("tasks/get", map[string]any{"taskId": id, "_meta": meta(token, true)})
	got, ok := polled["result"].(map[string]any)
	if !ok || got["taskId"] != id || got["status"] != "working" || got["statusMessage"] == "" {
		t.Fatalf("opaque task handle/status did not survive bridge: %v", polled)
	}
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"tasks/get", map[string]any{"taskId": id, "_meta": meta(token, false)}},
		{"tasks/get", map[string]any{"taskId": id, "_meta": meta("", true)}},
		{"subscriptions/listen", map[string]any{"_meta": meta(token, true)}},
		{"resources/subscribe", map[string]any{"uri": "dibs://inbox", "_meta": meta(token, true)}},
	} {
		if reply := call(tc.method, tc.params); reply["error"] == nil {
			t.Fatalf("missing per-request authority or forbidden push route was accepted: %s", tc.method)
		}
	}
}

func guestToolJSON(t *testing.T, reply map[string]any) map[string]any {
	t.Helper()
	result, _ := reply["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if reply["error"] != nil || result["isError"] == true || len(content) == 0 {
		t.Fatalf("real tool setup failed: %v", reply)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("tool did not carry server JSON: %v", err)
	}
	return value
}
