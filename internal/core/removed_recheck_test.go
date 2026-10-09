// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemovedRecheckAdmissionDoesNotRewriteHistoricalFold(t *testing.T) {
	s := NewState("old", DefaultLimits())
	reg(t, s, "worker", "worker-token", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "worker-token"}, t0)
	op := &Op{Kind: OpSetSlot, Token: "worker-token", SlotID: "s1", Text: "old wait", Waiting: "ci", RecheckSec: 1200}
	before := s.Serial
	if err := s.Admit(op); err == nil || !strings.Contains(err.Error(), "recheck_after was removed") {
		t.Errorf("new timed declaration not refused: %v", err)
	}
	if s.Serial != before {
		t.Fatal("admission mutated state")
	}
	op.AgentID = "worker" // serialized ledger ops carry the admitted actor, not its token
	encoded, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var historical Op
	if err := json.Unmarshal(encoded, &historical); err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, &historical, t0)
	if s.Agents["worker"].Slots["s1"].RecheckSec != 1200 || s.Serial != before+1 {
		t.Fatal("historical recheck did not fold unchanged")
	}
	for _, n := range []int{-1, 1, 1200} {
		op.RecheckSec = n
		if err := s.Admit(op); err == nil {
			t.Errorf("admitted retired recheck_sec=%d", n)
		}
	}
}
