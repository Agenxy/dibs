// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestBoardIdentityIsASnapshotAcrossLaterUpdates(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	mustApply(t, s, &Op{Kind: OpRegister, Name: "worker", NewToken: "token", Agent: &AgentInfo{Model: "original"}}, now)
	board := s.Board()
	identity := board["agents"].([]map[string]any)[0]["agent"].(*AgentInfo)
	mustApply(t, s, &Op{Kind: OpUpdate, Token: "token", Agent: &AgentInfo{Model: "changed"}}, now.Add(time.Second))
	if identity.Model != "original" {
		t.Fatal("board snapshot retained a live writer-owned identity pointer")
	}
	identity.Title = "reader's copy"
	if s.Agents["worker"].Agent.Title != "" {
		t.Fatal("board reader could mutate replayable identity through its result")
	}
}
