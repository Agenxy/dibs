// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
)

func TestAppShellBridgeCarriesTheInheritedThread(t *testing.T) {
	const shellThread = "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b"
	const namedThread = "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7c"
	t.Setenv("CODEX_THREAD_ID", shellThread)
	previousClient, previousDetect, previousProbe := lastClientInfo, detectedSurface, bridgeAncestry
	previousOwner := appShellThreadOwner
	previousThread := threadServed()
	t.Cleanup(func() {
		lastClientInfo, detectedSurface, bridgeAncestry = previousClient, previousDetect, previousProbe
		appShellThreadOwner = previousOwner
		noteThread(previousThread)
	})
	lastClientInfo = nil
	for _, tc := range []struct {
		name, surface, tool, want string
		meta, args                map[string]any
		nested                    bool
	}{
		{name: "shell", surface: harnessenv.ChatGPTApp, tool: "check_in", want: shellThread},
		{name: "nested harness", surface: harnessenv.ChatGPTApp, tool: "check_in", nested: true},
		{
			name: "native", surface: harnessenv.ChatGPTApp, tool: "check_in", want: namedThread,
			meta: map[string]any{"threadId": namedThread},
		},
		{
			name: "named session", surface: harnessenv.ChatGPTApp, tool: "check_in",
			meta: map[string]any{mcp.SessionMetaKey: "named-harness-session"},
		},
		{
			name: "stated activation", surface: harnessenv.ChatGPTApp, tool: "register",
			args: map[string]any{"name": "new-agent", "session_id": "named-new-activation"},
		},
		{name: "cloud", surface: harnessenv.ChatGPTCloudExec, tool: "check_in"},
		{name: "different harness", surface: harnessenv.ClaudeDesktop, tool: "check_in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridgeAncestry = &surfaceProbe{}
			detectedSurface = func(context.Context) (string, error) { return tc.surface, nil }
			appShellThreadOwner = func(int) bool { return !tc.nested }
			line, err := json.Marshal(map[string]any{"method": "tools/call", "params": map[string]any{
				"name": tc.tool, "arguments": tc.args, "_meta": tc.meta,
			}})
			if err != nil {
				t.Fatal("setup:", err)
			}
			var out struct {
				Params struct {
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if err := json.Unmarshal(enrichRegister(line), &out); err != nil {
				t.Fatal("decode actual bridge enrichment:", err)
			}
			if out.Params.Meta[mcp.SurfaceMetaKey] != tc.surface {
				t.Fatalf("setup: no observed surface in %v", out.Params.Meta)
			}
			if got, _ := out.Params.Meta["threadId"].(string); got != tc.want {
				t.Fatalf("bridge sent thread %q, want %q", got, tc.want)
			}
		})
	}
}
