// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestBridgeSurfaceEvidenceAdmissionIsNarrowAndReplayable(t *testing.T) {
	valid := func() *Op {
		return &Op{Kind: OpUpdate, Token: "worker-token", KeepDescription: true,
			Agent: &AgentInfo{Surface: "chatgpt-app"}, BridgeSurfaceEvidence: true, BridgeSurfaceTrusted: true}
	}
	if err := Admit(valid(), DefaultLimits()); err != nil {
		t.Fatalf("valid surface evidence refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Op)
	}{
		{"untrusted", func(op *Op) { op.BridgeSurfaceTrusted = false }},
		{"wrong-kind", func(op *Op) { op.Kind = OpAckBoard }},
		{"model", func(op *Op) { op.Agent.Model = "other" }},
		{"host", func(op *Op) { op.Agent.HostID = "other" }},
		{"name", func(op *Op) { op.Name = "other" }},
		{"description", func(op *Op) { op.Description = "other" }},
		{"release-session", func(op *Op) { op.ReleaseSession = true }},
		{"release-name", func(op *Op) { op.ReleaseNames = []string{"other"} }},
		{"no-process", func(op *Op) { op.NoProcess = true }},
		{"session", func(op *Op) { op.SessionAlias = "other" }},
		{"taken-session", func(op *Op) { op.SessionTakenFrom = "other" }},
		{"taken-alias", func(op *Op) { op.SessionAliasTakenFrom = "other" }},
		{"empty", func(op *Op) { op.Agent = &AgentInfo{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := valid()
			tc.edit(op)
			if err := Admit(op, DefaultLimits()); err == nil {
				t.Fatal("non-surface or untrusted evidence admitted")
			}
		})
	}
	now := time.Unix(1700000000, 0).UTC()
	reg := &Op{Kind: OpRegister, Name: "worker", Description: "keep", NewToken: "worker-token",
		Agent: &AgentInfo{Harness: "Codex", Model: "keep-model", Provider: "keep-provider", CWD: "/keep"}}
	s := NewState("test", DefaultLimits())
	mustApply(t, s, reg, now)
	regRaw, err := json.Marshal(reg) // the ledger freezes this before later identity merges
	if err != nil {
		t.Fatal(err)
	}
	op := valid()
	op.Description = "keep" // resolved by the writer, not participant input
	mustApply(t, s, op, now.Add(time.Minute))
	opRaw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if s.Agents["worker"].LastCoordination != now || s.Agents["worker"].Agent.Model != "keep-model" {
		t.Fatal("bridge evidence refreshed coordination or replaced identity")
	}
	// JSON drops both the live token and transient trust verdict. Replay must
	// fold the ledgered decision directly, without new ingress validation.
	replayed := NewState("test", DefaultLimits())
	for i, raw := range [][]byte{regRaw, opRaw} {
		var decoded Op
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		mustApply(t, replayed, &decoded, now.Add(time.Duration(i)*time.Minute))
	}
	if !reflect.DeepEqual(s, replayed) {
		t.Fatal("surface evidence fold diverged after JSON replay")
	}
	serial := s.Serial
	mustApply(t, s, op, now.Add(2*time.Minute))
	if s.Serial != serial {
		t.Fatal("identical bridge evidence allocated another serial")
	}
	ordinary := &Op{Kind: OpUpdate, Token: "worker-token", Description: "keep", Agent: &AgentInfo{Title: "model update"}}
	mustApply(t, s, ordinary, now.Add(3*time.Minute))
	if s.Agents["worker"].LastCoordination != now.Add(3*time.Minute) {
		t.Fatal("ordinary historical update lost its coordination semantics")
	}
}
