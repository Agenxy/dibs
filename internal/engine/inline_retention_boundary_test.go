// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestInlineRendererLeavesExpiredReviewSnapshotsUnread(t *testing.T) {
	e := New(core.NewState("retention-boundary", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.state.ReviewReadCutoff = 1
	expired := &core.Message{
		Serial: 2, From: "lead", To: "worker", State: core.MsgStateDone,
		RetainUntil: time.Now().Add(-time.Hour),
		Progress:    []core.Progress{{Serial: 3, By: "lead", Review: core.ReviewFlagged, Note: "expired flag"}},
	}
	units := e.reviewUnits(expired)
	if len(units) != 0 {
		t.Fatal("setup: an expired review must yield an empty snapshot")
	}
	// Expiry between receivedReviews' eligibility check and outcomeGroups'
	// fresh snapshot can pass an empty group to this shared production
	// renderer. Fix the boundary without a timing-dependent race probe.
	groups := []outcomeGroup{
		{agent: "worker", message: expired, units: units},
		{agent: "worker", message: &core.Message{Serial: 4}, units: []outcomeUnit{
			{serial: 5, text: "live review", body: "still retained"},
		}},
		{agent: "worker", message: &core.Message{Serial: 6}},
	}
	budget := mailQuoteBudget
	lines, through := e.presentGroupedOutcomes(groups, &budget, nil)
	if len(lines["worker"]) != 1 || !strings.Contains(lines["worker"][0], "still retained") {
		t.Fatalf("empty snapshot changed retained presentation: %v", lines)
	}
	if len(through["worker"]) != 1 || through["worker"][4] != 5 {
		t.Fatalf("expired/unquoted snapshot advanced a read prefix: %v", through)
	}
	if budget != mailQuoteBudget-len("still retained") {
		t.Fatalf("empty snapshot consumed body budget: %d", budget)
	}
}
