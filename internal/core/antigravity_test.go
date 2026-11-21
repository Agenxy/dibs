// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestAntigravityAttestationDoesNotFollowAnAliasBack(t *testing.T) {
	const a = "01a10520-0000-7000-8000-000000000091"
	const b = "01a10520-0000-7000-8000-000000000092"
	for _, tt := range []struct {
		name string
		ops  []Op
	}{
		{"session round trip", []Op{
			{Kind: OpBindSession, SessionID: b}, {Kind: OpBindSession, SessionID: a},
		}},
		{"release and bind", []Op{
			{Kind: OpUpdate, ReleaseSession: true}, {Kind: OpBindSession, SessionID: a},
		}},
		{"host round trip", []Op{
			{Kind: OpUpdate, Agent: &AgentInfo{HostID: "other"}},
			{Kind: OpUpdate, Agent: &AgentInfo{HostID: "local"}},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := NewState("local", DefaultLimits())
			apply := func(op Op) {
				t.Helper()
				if _, _, err := s.Apply(&op, time.Unix(10, 0)); err != nil {
					t.Fatal(err)
				}
			}
			apply(Op{
				Kind: OpRegister, Name: "worker", NewToken: "token", SessionAlias: a,
				Agent: &AgentInfo{HostID: "local", Harness: "antigravity"}, V7Semantics: true,
			})
			proof := Op{Kind: OpAntigravityBinding, Token: "token", SessionID: a}
			apply(proof)
			serial := s.Serial
			apply(proof)
			row := s.Agents["worker"]
			if row.AntigravityBindingSerial != serial || s.Serial != serial {
				t.Fatal("repeat observation rewrote the attestation")
			}
			for _, op := range tt.ops {
				op.Token, op.V7Semantics = "token", true
				apply(op)
			}
			if row.CurrentSession != a || row.Agent.HostID != "local" || row.AntigravityBindingSerial != 0 {
				t.Fatal("ordinary operations restored old app attestation")
			}
			apply(proof)
			if row.AntigravityBindingSerial <= serial {
				t.Fatal("a new app observation did not get a fresh fence")
			}
		})
	}
}
