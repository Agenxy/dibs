// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestReplayedHumanIsExcludedFromAgentStallTracking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	journal := &memLedger{}
	e := New(core.NewState("t", core.DefaultLimits()), journal, deadProber{})
	go e.Run(ctx)
	human, token, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup human:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSetSlot, SlotID: "s1", Token: token, Text: "human's own work"}); err != nil {
		t.Fatal("setup declaration:", err)
	}
	restored := New(replayed(t, ctx, e, journal), &memLedger{}, deadProber{})
	// The real tick decision receives replayed state and an empty credential
	// cache, exactly as at boot. No human flag/cache is set by the fixture.
	restored.stallTick(time.Now().Add(time.Hour))
	if _, tracked := restored.wakers.work[human]; tracked {
		t.Fatal("replayed human entered agent stall tracking before another unlock")
	}
}
