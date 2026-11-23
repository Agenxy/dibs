// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestBoardTurnFromClaudeHooks(t *testing.T) {
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.Run(ctx)

	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	reg, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", SessionID: thread,
		Agent: &core.AgentInfo{Harness: "Claude Code"},
	})
	if err != nil || reg["token"] == nil {
		t.Fatalf("setup: register: %v %v", reg, err)
	}
	for _, step := range []struct {
		event, turn string
	}{
		{"", "unknown"},
		{"SessionStart", "unknown"},
		{"UserPromptSubmit", "in_turn"},
		{"PermissionRequest", "in_turn"},
		{"Stop", "between_turns"},
		{"SessionEnd", "unknown"},
	} {
		t.Run(step.event, func(t *testing.T) {
			if step.event != "" {
				if _, err := e.HookPoll(ctx, thread, step.event, "", false, false); err != nil {
					t.Fatalf("setup: hook: %v", err)
				}
			}
			board, err := e.Board(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rows := board["agents"].([]map[string]any)
			if len(rows) != 1 || rows[0]["id"] != "worker" || rows[0]["status"] != core.StatusActive {
				t.Fatalf("setup: expected one active worker, got %v", rows)
			}
			if got := rows[0]["turn"]; got != step.turn {
				t.Errorf("board turn = %v, want %q", got, step.turn)
			}
		})
	}
}
