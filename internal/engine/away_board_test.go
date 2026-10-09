// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// A queued notice is not evidence that a turn started. Drive the actual app
// recovery door and then read the real decorated board after the open.
func TestBoardDoesNotClaimAnAwayWaitAfterImmediateClaudeRecovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "fixture", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local_away-fixture.json"), []byte(`{"sessionId":"local_away-fixture","cliSessionId":"01876543-1234-4567-8901-234567890abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := shower
	t.Cleanup(func() { shower = original })
	opens, waits := 0, 0
	shower = &harnessenv.Shower{
		Holds: func(string) bool { return false },
		Open:  func([]string) error { opens++; return nil },
		Wait:  func(time.Duration) { waits++; t.Error("Claude recovery was deferred") },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := New(core.NewState("fixture", core.DefaultLimits()), &memLedger{}, deadProber{})
	go e.Run(ctx)
	const thread = "01876543-1234-4567-8901-234567890abc"
	reg, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "away-worker", SessionID: thread,
		Agent: &core.AgentInfo{Harness: "Claude Code", Surface: harnessenv.ClaudeDesktop},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	id := reg["agent_id"].(string)
	e.showInApp(wakePlan{thread: thread, harness: "Claude Code", surface: harnessenv.ClaudeDesktop}, id)
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == id {
			status, _ = row["wake"].(string)
		}
	}
	if opens != 1 || waits != 0 {
		t.Fatalf("recovery did not open immediately: opens=%d waits=%d", opens, waits)
	}
	if strings.Contains(status, "away") {
		t.Fatalf("board invented an away wait: wake=%q", status)
	}
}
