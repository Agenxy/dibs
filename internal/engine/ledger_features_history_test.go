// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

type featureHistoryLedger struct {
	*ledger.Ledger
	t          *testing.T
	lastKind   string
	lastSerial uint64
	sawConsent bool
}

func (l *featureHistoryLedger) Append(serial uint64, at time.Time, op *core.Op) error {
	err := l.Ledger.Append(serial, at, op)
	if err == nil {
		l.lastKind, l.lastSerial = op.Kind, serial
	}
	return err
}

func (l *featureHistoryLedger) ObserveMail(before mailhistory.Snapshot, st *core.State, op *core.Op, events []core.Event) {
	if op.Kind != l.lastKind {
		l.t.Errorf("nested marker replaced the original history record: observing %s on %s record", op.Kind, l.lastKind)
	}
	if op.Kind == core.OpSendMessage && op.Recovery != nil {
		l.sawConsent = true
		if before.Authors[op.AgentID].ID != op.AgentID {
			l.t.Error("nested marker cleared the original author's capture")
		}
		for _, message := range st.Messages {
			if message.Recovery != nil && message.Serial != l.lastSerial {
				l.t.Errorf("consent history serial %d, want its native record %d", message.Serial, l.lastSerial)
			}
		}
	}
	l.Ledger.ObserveMail(before, st, op, events)
}

// Also run with prediction removed: the post-append marker must not replace
// the native record receipt or reuse its author's snapshot before observation.
func TestReaderBackstopPreservesOriginalMailHistoryRecord(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	disk, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "fence", box)
	if err != nil {
		t.Fatal(err)
	}
	spy := &featureHistoryLedger{Ledger: disk, t: t}
	e := New(core.NewState("fence", core.DefaultLimits()), spy, deadProber{})
	e.SetHumanNotifier(noContactDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.Done(); _ = disk.Close() })
	tokens := populateRecoveryFenceBoard(t, e, ctx)
	plan, err := e.RecoverAgent(ctx, tokens["survivor"], "source", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatal("setup preview:", err)
	}
	if _, err := e.RecoverAgent(ctx, tokens["survivor"], "source", MergeOptions{Fence: plan["fence"].(string)}); err != nil {
		t.Fatal("setup recovery:", err)
	}
	if _, err := e.query(ctx, func() core.Result {
		if !spy.sawConsent {
			t.Error("setup: recovery consent never entered the history observer")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
