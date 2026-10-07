// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"testing"

	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
)

// A Codex bridge that is NOT under the ChatGPT app says so on every call,
// rather than saying nothing.
//
// Identity fields merge, so a blank changes nothing on the board. A thread
// started in the app and then run from a terminal would keep reading as an app
// thread, and every wake would open the app on a thread the operator is
// driving somewhere else. Entered through the real sequence: initialize names
// the client, then a tools/call goes through enrichRegister.
func TestACodexOutsideTheAppSaysWhereItIs(t *testing.T) {
	prev, prevDetect := lastClientInfo, detectedSurface
	t.Cleanup(func() { lastClientInfo, detectedSurface = prev, prevDetect })
	// The process tree is whatever ran the suite, which on a developer's
	// machine is often an app: this one ran under Claude's and read as it.
	detectedSurface = func() string { return "" }
	stamp := func(client string) any {
		t.Helper()
		enrichRegister([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"clientInfo":{"name":"` + client + `"}}}`))
		out := enrichRegister([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_in","arguments":{}}}`))
		var msg struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(out, &msg); err != nil {
			t.Fatalf("setup: the enriched call is not JSON: %v", err)
		}
		if msg.Params.Meta == nil {
			t.Fatal("setup: the bridge stamped no _meta at all, so this proves nothing")
		}
		return msg.Params.Meta[mcp.SurfaceMetaKey]
	}
	if got := stamp("codex-mcp-client"); got != harnessenv.CodexOutsideApp {
		t.Errorf("a Codex outside the app stamped %v: a thread that moved out of the app "+
			"would keep being opened there", got)
	}
	if got := stamp("claude-code"); got != nil {
		t.Errorf("a Claude Code bridge stamped %v: it is not Codex, and nothing derived says otherwise", got)
	}
}

// And the tree wins over the client name: a Codex under the app is the app.
func TestTheProcessTreeNamesTheAppBeforeTheClientDoes(t *testing.T) {
	prev, prevDetect := lastClientInfo, detectedSurface
	t.Cleanup(func() { lastClientInfo, detectedSurface = prev, prevDetect })
	detectedSurface = func() string { return harnessenv.ChatGPTApp }
	lastClientInfo = map[string]any{"name": "codex-mcp-client"}
	if got := bridgeSurface(); got != harnessenv.ChatGPTApp {
		t.Errorf("a Codex under the ChatGPT app stated %q: its wakes would never open the app", got)
	}
}
