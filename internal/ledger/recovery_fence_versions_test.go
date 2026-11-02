// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestRecoveryProposalVersionsPreserveRecordedFencesAcrossReplaySnapshotCompaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, "recovery-versions", box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s := core.NewState("recovery-versions", core.DefaultLimits())
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"source", "survivor", "lead"} {
		apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: id, Nonce: id + "-nonce", NewToken: id + "-token", AgentKind: core.KindPersistent}, old)
	}
	// This is a trusted historical fold, not current permission admission.
	// Apply recalculates after touching the sender, so the recorded prefix is
	// the version discriminant, independently of the placeholder digest bytes.
	ids := []uint64{}
	for _, prefix := range []string{"", "v2:"} {
		r := &core.RecoveryRequest{From: "source", Into: "survivor", SourceCreated: s.Agents["source"].CreatedSerial, SurvivorCreated: s.Agents["survivor"].CreatedSerial, CoordinatorCreated: s.Agents["lead"].CreatedSerial, Fence: prefix + "sha256:" + strings.Repeat("a", 64)}
		result := apply(t, s, l, &core.Op{Kind: core.OpSendMessage, Token: "lead-token", To: "survivor", MsgType: core.MsgRequest, Body: "versioned proposal", Recovery: r}, old)
		ids = append(ids, result["msg_serial"].(uint64))
	}
	fences := func(st *core.State) []string {
		return []string{st.Messages[ids[0]].Recovery.Fence, st.Messages[ids[1]].Recovery.Fence}
	}
	want := fences(s)
	if strings.HasPrefix(want[0], "v2:") || !strings.HasPrefix(want[1], "v2:") {
		t.Fatalf("recorded v1/v2 versions lost: %v", want)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 4; pass++ {
		l, err = Open(path, "recovery-versions", box)
		if err != nil {
			t.Fatal(err)
		}
		if pass >= 2 {
			l.KeepHistory(time.Hour)
			l.clock = func() time.Time { return old.Add(72 * time.Hour) }
		}
		s = core.NewState("recovery-versions", core.DefaultLimits())
		if _, err := l.Replay(s); err != nil {
			t.Fatal(err)
		}
		if got := fences(s); !reflect.DeepEqual(got, want) {
			t.Fatalf("pass%d rewrote versioned fences: got%v want%v", pass, got, want)
		}
		if pass == 0 {
			if err := writeSnapshot(filepath.Join(dir, SnapshotName), SnapshotHeader{Serial: s.Serial, Head: l.headHash, Node: s.NodeID, Created: old}, s, box); err != nil {
				t.Fatal(err)
			}
		}
		if pass == 1 {
			// Append behind the loaded snapshot, then let KeepHistory's real
			// replay path capture and remove it on the next pass. This proves
			// compaction ran, rather than merely reopening an existing snapshot.
			apply(t, s, l, &core.Op{Kind: core.OpAckBoard, Token: "lead-token"}, old.Add(time.Minute))
		}
		if pass >= 2 {
			if h, ok, err := readSnapshotHeader(filepath.Join(dir, SnapshotName)); err != nil || !ok || h.Serial != s.Serial {
				t.Fatalf("compaction snapshot missing or wrong: %v", err)
			}
			if info, err := os.Stat(path); err != nil || info.Size() != 0 {
				t.Fatalf("old ledger was not physically compacted: info=%v err=%v", info, err)
			}
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
