// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A security preference must not vanish when old gob readers ignore its new
// field. Exercise real compaction, including an empty retained ledger, and
// optionally enter an independently built old daemon's read-only preflight.
func TestDesktopApprovalSnapshotRetainsPolicyAndRefusesOldReaders(t *testing.T) {
	l, path := newLedger(t)
	dir := filepath.Dir(path)
	for name, body := range map[string]string{"node_id": "test", "local.secret": strings.Repeat("a", 64)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st := core.NewState("test", core.DefaultLimits())
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "policy-member", NewToken: "member-token", Nonce: "member-nonce"}, t0)
	apply(t, st, l, &core.Op{
		Kind: core.OpSetDesktopApprovalSetting, Token: "member-token",
		SettingKey: core.DesktopApprovalSettingKey, SettingValue: "true",
	}, t0.Add(time.Second))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	compacting, err := Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	compacting.KeepHistory(time.Hour)
	compacting.clock = func() time.Time { return t0.Add(48 * time.Hour) }
	got := core.NewState("test", core.DefaultLimits())
	if _, err := compacting.Replay(got); err != nil {
		t.Fatal(err)
	}
	_, anchored := compacting.Anchor()
	if err := compacting.Close(); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(path)
	if err != nil || !anchored || len(kept) != 0 {
		t.Fatalf("setup did not compact the policy entirely into a snapshot: anchor=%v kept=%d err=%v", anchored, len(kept), err)
	}
	if got.DesktopApproval != st.DesktopApproval || !got.DesktopApproval.Enabled {
		t.Fatalf("compaction lost the policy/provenance: %+v, want %+v", got.DesktopApproval, st.DesktopApproval)
	}
	header, present, err := SnapshotAnchor(path)
	if err != nil || !present {
		t.Fatalf("read actual on-disk snapshot header: %+v %v", header, err)
	}
	if old := os.Getenv("DIBS_TEST_APPROVAL_OLD_DAEMON"); old != "" {
		cmd := exec.Command(old, "-dir", dir, "-check", "-addr", "127.0.0.1:4777")
		cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_NOTIFY=off")
		output, err := cmd.CombinedOutput()
		t.Logf("old daemon preflight: err=%v output=%s", err, output)
		if err == nil || !strings.Contains(string(output), "snapshot version 2") {
			t.Errorf("old daemon must refuse the unknown security snapshot, not accept or fail for another reason: %v %s", err, output)
		}
	}
	if header.Version != 2 {
		t.Fatalf("security policy snapshot has version %d; a version-1 reader would silently drop it", header.Version)
	}
}
