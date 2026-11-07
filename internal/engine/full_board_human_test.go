// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestFullBoardHumanMintDoesNotNeedAnAgentSeat(t *testing.T) {
	for _, policy := range []string{core.FullBoardDisplaceIdle, core.FullBoardRefuse} {
		t.Run(policy, func(t *testing.T) {
			st := capacityState(t, "worker")
			e := New(st, &memLedger{}, fullBoardProber{100: true})
			if err := e.SetFullBoardPolicyDefault(policy); err != nil {
				t.Fatal(err)
			}
			ctx := runCapacityEngine(t, e)
			id, token, err := e.HumanAgent(ctx)
			if err != nil || id == "" || token == "" {
				t.Fatalf("full board refused its human identity: %v", err)
			}
			onLoop(t, ctx, e, func(s *core.State) {
				if s.Agents["worker"].Status != core.StatusDormant {
					t.Error("human mint displaced an agent seat")
				}
			})
		})
	}
}

func TestFullBoardHumanDoesNotConsumeEitherAgentCeiling(t *testing.T) {
	st := capacityState(t, "worker")
	st.Limits.MaxAgents, st.Limits.MaxPersistentAgents = 2, 2
	e := New(st, &memLedger{}, nil)
	ctx := runCapacityEngine(t, e)
	if _, _, err := e.HumanAgent(ctx); err != nil {
		t.Fatal("human setup:", err)
	}
	if _, err := e.Do(ctx, capacityRegister()); err != nil {
		t.Fatal("human consumed an agent ceiling:", err)
	}
	onLoop(t, ctx, e, func(s *core.State) {
		if s.Agents["worker"].Status != core.StatusDormant {
			t.Error("human consumed a ceiling and caused an unnecessary displacement")
		}
	})
}
