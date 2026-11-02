// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestRecoveryProposalVersionsPreserveRecordedFencesAcrossReplaySnapshotCompaction(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"node_id": "recovery-versions", "local.secret": strings.Repeat("a", 64)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	for _, tc := range []struct{ prefix, from, to string }{
		{"", "lead", "survivor"},
		{"v2:", "lead", "survivor"},
		{"v3:", "lead", "survivor"},
		{"v3:", "survivor", "lead"},
	} {
		r := &core.RecoveryRequest{From: "source", Into: "survivor", SourceCreated: s.Agents["source"].CreatedSerial, SurvivorCreated: s.Agents["survivor"].CreatedSerial, CoordinatorCreated: s.Agents["lead"].CreatedSerial, Fence: tc.prefix + "sha256:" + strings.Repeat("a", 64)}
		result := apply(t, s, l, &core.Op{Kind: core.OpSendMessage, Token: tc.from + "-token", To: tc.to, MsgType: core.MsgRequest, Body: "versioned proposal", Recovery: r}, old)
		ids = append(ids, result["msg_serial"].(uint64))
	}
	fences := func(st *core.State) []string {
		var result []string
		for _, id := range ids {
			result = append(result, st.Messages[id].Recovery.Fence)
		}
		return result
	}
	want := fences(s)
	if strings.HasPrefix(want[0], "v2:") || !strings.HasPrefix(want[1], "v2:") {
		t.Fatalf("recorded v1/v2 versions lost: %v", want)
	}
	if !strings.HasPrefix(want[2], "v3:") || !strings.HasPrefix(want[3], "v3:") {
		t.Fatal("both v3 recorded directions must retain their prefix")
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
			assertV3SnapshotRequirement(t, dir)
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
			assertV3SnapshotRequirement(t, dir)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertV3SnapshotRequirement(t *testing.T, dir string) {
	t.Helper()
	snapshot := filepath.Join(dir, SnapshotName)
	header, ok, err := readSnapshotHeader(snapshot)
	if err != nil || !ok {
		t.Fatal("actual snapshot is missing", err)
	}
	found := false
	for _, name := range header.Features {
		found = found || name == "mailbox-recovery-consent/v3"
	}
	if !found {
		t.Fatal("snapshot with v3 consent did not declare its reader requirement")
	}
	old := os.Getenv("DIBS_TEST_RECOVERY_V3_OLD_DAEMON")
	if old == "" {
		return
	}
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, old, "-dir", dir, "-check", "-addr", "127.0.0.1:4777")
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_NOTIFY=off")
	out, runErr := cmd.CombinedOutput()
	t.Logf("old snapshot reader err=%v output=%s", runErr, out)
	if runErr == nil || !strings.Contains(string(out), "mailbox-recovery-consent/v3") {
		t.Fatalf("old snapshot reader did not refuse the v3 format: %v %s", runErr, out)
	}
	after, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("old reader changed the snapshot bytes", err)
	}
}
