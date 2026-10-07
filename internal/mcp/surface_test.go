// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// The app an agent runs in decides where a wake may go, so it comes from the
// bridge's reading of the process tree and never from the agent's own word.
//
// The case that matters is the second: a Codex in a terminal stating that it
// runs in the ChatGPT app would have every wake open the operator's app on a
// thread they are driving somewhere else, which is the relocation the operator
// forbade. A stated surface Dibs does not act on is still kept, because for a
// caller with no bridge it is the only source there is.
func TestTheAppAnAgentRunsInIsDerivedNotStated(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	surfaceOf := func(id string) string {
		t.Helper()
		b, err := eng.Board(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		agents, _ := b["agents"].([]map[string]any)
		for _, a := range agents {
			if a["id"] == id {
				info, _ := a["agent"].(*core.AgentInfo)
				if info == nil {
					return ""
				}
				return info.Surface
			}
		}
		t.Fatalf("agent %s is not on the board, so the setup did not happen", id)
		return ""
	}

	reg := toolCallWithMeta(t, srv, "register", map[string]any{"name": "in-app", "cwd": "/w"},
		map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp})
	if reg["token"] == nil {
		t.Fatalf("setup: %v", reg)
	}
	if got := surfaceOf("in-app"); got != harnessenv.ChatGPTApp {
		t.Errorf("the bridge derived the ChatGPT app and the board recorded %q: its wakes "+
			"would queue into a thread the app never loads", got)
	}

	reg = toolCall(t, srv, "register", map[string]any{
		"name": "claims-the-app", "cwd": "/w", "surface": harnessenv.ChatGPTApp,
	})
	if reg["token"] == nil {
		t.Fatalf("setup: %v", reg)
	}
	if got := surfaceOf("claims-the-app"); got != "" {
		t.Errorf("an agent that only SAID it runs in the ChatGPT app was recorded as %q: "+
			"a wake would open the app on its thread", got)
	}

	reg = toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "terminal", "cwd": "/w", "surface": harnessenv.ChatGPTApp,
	}, map[string]any{SurfaceMetaKey: harnessenv.CodexOutsideApp})
	if reg["token"] == nil {
		t.Fatalf("setup: %v", reg)
	}
	if got := surfaceOf("terminal"); got != harnessenv.CodexOutsideApp {
		t.Errorf("the bridge said a terminal Codex and the agent said the app; the board "+
			"recorded %q, and the derived answer has to win", got)
	}

	reg = toolCall(t, srv, "register", map[string]any{"name": "cli", "cwd": "/w", "surface": "cli"})
	if reg["token"] == nil {
		t.Fatalf("setup: %v", reg)
	}
	if got := surfaceOf("cli"); got != "cli" {
		t.Errorf("a stated surface Dibs never acts on was dropped (%q): for a caller "+
			"with no bridge it is the only source", got)
	}
}
