// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestMergeCredentialTombstoneSurvivesSnapshotAndSourceRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s := core.NewState("test", core.DefaultLimits())
	apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: "fork", Nonce: "fork-secret", NewToken: "fork-token"}, old)
	apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: "survivor", Nonce: "survivor-secret", NewToken: "survivor-token"}, old)
	apply(t, s, l, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"fork"}}, old.Add(time.Minute))
	apply(t, s, l, &core.Op{
		Kind: core.OpMergeAgents, To: "fork", MergeInto: "survivor",
		MergeActor: &core.MergeActor{ID: "coordinator", Authority: "coordinator", Evidence: core.MergeEvidence{Known: true}},
	}, old.Add(2*time.Minute))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Compact away the original merge op. Recovery must use the encrypted
	// snapshot, rather than accidentally reconstructing from retained JSONL.
	for pass := 0; pass < 2; pass++ {
		l, err = Open(path, "test", box)
		if err != nil {
			t.Fatal(err)
		}
		l.KeepHistory(time.Hour)
		l.clock = func() time.Time { return old.Add(72 * time.Hour) }
		s = core.NewState("test", core.DefaultLimits())
		if _, err := l.Replay(s); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := readSnapshotHeader(filepath.Join(dir, SnapshotName)); err != nil || !ok {
			t.Fatalf("setup: no snapshot: %v", err)
		}
		// Even after source identity retention, its credential cannot mint a
		// replacement row. The canonical survivor credential still works.
		delete(s.Agents, "fork")
		before := s.Serial
		if err := s.Admit(&core.Op{Kind: core.OpRegister, Name: "fork", Nonce: "fork-secret"}); err == nil {
			t.Fatal("source-only credential accepted after snapshot/retention")
		}
		if s.Serial != before {
			t.Fatal("refusal changed replayable state")
		}
		if err := s.Admit(&core.Op{Kind: core.OpRegister, Name: "survivor", Nonce: "survivor-secret"}); err != nil {
			t.Fatal("survivor recovery refused", err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
