// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestStalledWorkWatermarkSurvivesConsumedMailAndCompaction(t *testing.T) {
	l, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	for _, id := range []string{"worker", "lead", "reporter"} {
		nonce := id + "-nonce"
		if id == "reporter" {
			nonce = core.DibsNonce
		}
		apply(t, st, l, &core.Op{
			Kind: core.OpRegister, Name: id, NewToken: id, Nonce: nonce,
			AgentKind: core.KindPersistent, SessionID: id + "-session",
		}, t0)
		apply(t, st, l, &core.Op{Kind: core.OpAckBoard, Token: id}, t0)
	}
	apply(t, st, l, &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator}, t0)
	apply(t, st, l, &core.Op{Kind: core.OpSetSlot, Token: "worker", SlotID: "s1", Text: "verify upgrade"}, t0)
	op := core.Op{
		Kind: core.OpStalledWorkObserved, StalledWorker: "worker", SessionID: "worker-session",
		DeclarationSerial: st.Serial,
	}
	observed := t0.Add(time.Hour)
	apply(t, st, l, &op, observed)
	op.Kind, op.Token, op.To, op.Body = core.OpStalledWorkNotified, "reporter", "lead", "Stalled work for worker"
	op.MsgType, op.MailboxCapacity = core.MsgNotify, true
	if err := st.Admit(&op); err != nil {
		t.Fatalf("setup notice: %v", err)
	}
	serial := apply(t, st, l, &op, observed.Add(time.Minute))["msg_serial"].(uint64)
	apply(t, st, l, &core.Op{Kind: core.OpAckMessage, Token: "lead", MsgSerial: serial}, observed.Add(2*time.Minute))
	apply(t, st, l, &core.Op{Kind: core.OpSweep}, observed.Add(2*time.Hour))
	if st.Messages[serial] != nil {
		t.Fatal("setup: consumed notice survived retention")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st = replayCompactedStalledWork(t, path)
		marker := st.Agents["worker"].StalledWorkNotice
		if marker == nil || marker.NoticeSerial != serial || marker.DeclarationSerial != op.DeclarationSerial ||
			!marker.ObservedAt.Equal(observed) || marker.Session != op.SessionID {
			t.Fatalf("lost the original stall receipt: %+v", marker)
		}
		before := st.Serial
		result, _, err := st.Apply(&op, observed.Add(4*time.Hour))
		if err != nil || st.Serial != before || result["deduplicated"] != true {
			t.Fatalf("the consumed notice was recreated: %v / %v", result, err)
		}
	}
}

func replayCompactedStalledWork(t *testing.T, path string) *core.State {
	t.Helper()
	box, err := LoadOrCreateKey(filepath.Join(filepath.Dir(path), "key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	l.KeepHistory(time.Hour)
	l.clock = func() time.Time { return t0.Add(72 * time.Hour) }
	st := core.NewState("test", core.DefaultLimits())
	if _, err := l.Replay(st); err != nil {
		t.Fatal(err)
	}
	anchor, ok := l.Anchor()
	if !ok || !slices.Contains(anchor.Features, "stalled-work-notice/v1") {
		t.Fatalf("snapshot did not require the reader feature: %+v", anchor)
	}
	return st
}
