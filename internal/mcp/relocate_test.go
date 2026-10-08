// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The relocate tool reaches the engine with the agent and the environment in
// their own places, and the permission is checked through the tool. A swapped
// pair would report "no agent chatgpt-app" rather than naming the ghost.
func TestTheRelocateToolCarriesTheAgentAndTheEnvironment(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	reg := toolCall(t, srv, "register", map[string]any{"name": "lead", "cwd": "/w"})
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("setup: %v", reg)
	}
	member := toolCall(t, srv, "register", map[string]any{"name": "member", "cwd": "/w"})
	mtok, _ := member["token"].(string)

	refused := toolCall(t, srv, "relocate", map[string]any{"token": mtok, "agent": "lead", "environment": "chatgpt-app"})
	if !strings.Contains(fmt.Sprint(refused), "E_NOT_PERMITTED") {
		t.Fatalf("a member was not refused the permission: %v", refused)
	}

	if _, err := eng.GrantRoleByHuman(context.Background(), "lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out := toolCall(t, srv, "relocate", map[string]any{"token": tok, "agent": "ghost-agent", "environment": "chatgpt-app"})
	if !strings.Contains(fmt.Sprint(out), "ghost-agent") {
		t.Errorf("the agent argument did not reach the engine as the agent: %v", out)
	}
}
