// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestStoppedOpenWorkNeverStartsATimerWake(t *testing.T) {
	if _, err := os.Stat("/usr/bin/touch"); err != nil {
		t.Skip("no touch fixture")
	}
	b := newContinuationBoard(t)
	b.declare(t, "unfinished implementation", "")
	b.wake(t)
	for range maxContinuations {
		if !continued(b.stop(t, false)) {
			t.Fatal("setup: Stop continuation missing")
		}
	}
	if continued(b.stop(t, false)) {
		t.Fatal("setup: continuation was not bounded")
	}
	b.e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/touch", "{thread}"}, Cooldown: time.Millisecond}})
	var marker string
	setup, err := b.e.query(b.ctx, func() core.Result {
		l := b.e.state.Agents["worker"]
		marker = filepath.Join(l.Agent.CWD, contThread)
		ended := time.Now().Add(-2 * time.Hour)
		b.e.turnEnded[l.ID] = ended
		b.e.seen[l.ID], b.e.hookAlive[l.ID] = ended.Add(-time.Second), ended
		l.LastCoordination = ended.Add(-time.Second)
		b.e.wakers.mu.Lock()
		_, started := b.e.wakers.dibsTurn[l.ID]
		running := b.e.wakers.running[l.ID]
		b.e.wakers.mu.Unlock()
		return core.Result{
			"started": started, "running": running,
			"quiet": !b.e.lastEvidenceOf(l).After(ended), "lifecycle": b.e.socketLifecycle(l, time.Now()),
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if setup["started"] != true || setup["running"] != false || setup["quiet"] != true || setup["lifecycle"] != "idle" {
		t.Fatal("setup: stopped Dibs-started quiet turn was not established", setup)
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// The production Run loop owns the tick, including on the old code.
	<-time.After(17500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("timer woke stopped open work")
	}
}

func TestHistoricalWaitingNeverStartsASocketTimerWake(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.token})
	result, err := f.e.query(f.ctx, func() core.Result {
		// Replay's door: old accepted ops must still fold, never re-admit them.
		_, err := f.e.applyAndLedger(&core.Op{
			Kind: core.OpSetSlot, Token: f.token,
			Text: "historical timed wait", Waiting: "ci", RecheckSec: 1,
		}, time.Now())
		return core.Result{"error": err}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["error"] != nil {
		t.Fatal("setup historical fold:", result["error"])
	}
	f.hook(t, "Stop", true)
	select {
	case text := <-f.wire:
		t.Fatalf("historical waiting started a timer wake: %q", text)
	case <-time.After(2500 * time.Millisecond):
	}
}
