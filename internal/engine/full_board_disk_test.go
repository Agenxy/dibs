// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func TestFullBoardRegistrationAndPolicySurviveEncryptedReplay(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	disk, err := ledger.Open(path, "capacity-replay", box)
	if err != nil {
		t.Fatal(err)
	}
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents = 3, 3
	st := core.NewState("capacity-replay", limits)
	old := time.Now().Add(-time.Hour)
	appendOld := func(op *core.Op, at time.Time) {
		t.Helper()
		before := st.Serial
		if _, _, err := st.Apply(op, at); err != nil {
			t.Fatal("historical setup:", err)
		}
		if st.Serial != before+1 {
			t.Fatal("historical setup did not advance")
		}
		if err := disk.Append(st.Serial, at, op); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"oldest", "newer", "lead"} {
		appendOld(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "nonce-" + id, NewToken: "token-" + id, AgentKind: core.KindPersistent}, old.Add(time.Duration(i)*time.Minute))
	}
	appendOld(&core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator}, old.Add(3*time.Minute))
	appendOld(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"oldest", "newer"}}, old.Add(4*time.Minute))
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	// Old records have no displacement fields and must replay unchanged.
	disk, err = ledger.Open(path, "capacity-replay", box)
	if err != nil {
		t.Fatal(err)
	}
	st = core.NewState("capacity-replay", limits)
	if _, err := disk.Replay(st); err != nil {
		t.Fatal(err)
	}
	if st.Agents["oldest"].Status != core.StatusDormant || len(st.RequiredFeatures) != 0 {
		t.Fatal("old history changed")
	}
	e := New(st, disk, nil)
	if err := e.SetFullBoardPolicyDefault(core.FullBoardRefuse); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	go e.Run(ctx)
	defer cancel()
	if _, err := e.Configure(ctx, "token-lead", core.FullBoardSettingKey, core.FullBoardDisplaceIdle); err != nil {
		t.Fatal(err)
	}
	op := capacityRegister()
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatal(err)
	}
	if op.Displaced == nil || op.Displaced.ID != "oldest" {
		t.Fatalf("recorded decision: %+v", op.Displaced)
	}
	var want []byte
	onLoop(t, ctx, e, func(s *core.State) { want, err = json.Marshal(s) })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-e.done
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 3; pass++ {
		disk, err = ledger.Open(path, "capacity-replay", box)
		if err != nil {
			t.Fatal(err)
		}
		if pass > 0 {
			disk.KeepHistory(time.Nanosecond)
		}
		fold := core.NewState("capacity-replay", limits)
		if _, err := disk.Replay(fold); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(fold)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("pass %d: encrypted replay/snapshot changed recorded victim or state", pass)
		}
		if err := fold.MergedNonceError("nonce-oldest"); err == nil {
			t.Fatal("old nonce recovered a displaced seat")
		}
		delete(fold.Agents, "oldest")
		if err := fold.MergedNonceError("nonce-oldest"); err == nil {
			t.Fatal("nonce refusal depended on victim retention")
		}
		if err := disk.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
