// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func TestNativeRestartPreInputFailureUsesColdQueue(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	s := testcodexipc.Start(t, "changed", nil)
	e, ctx, _, _ := nativeAppEngine(t)
	e.SetAppRestartDefaults(time.Hour, time.Hour, true, true)
	previousEpoch, previousQueue, previousShower := appRestartEpoch.Load(), restartQueue, shower
	appRestartEpoch.Store(func() (string, bool) { return "app:new", true })
	t.Cleanup(func() { appRestartEpoch.Store(previousEpoch); restartQueue, shower = previousQueue, previousShower })
	queued, opened := make(chan struct{}, 2), make(chan struct{}, 2)
	restartQueue = func(_ []string, _, _ string) wakeexec.RestartQueueOutcome {
		queued <- struct{}{}
		return wakeexec.RestartQueueOutcome{OK: true}
	}
	shower = &harnessenv.Shower{
		Holds: func(string) bool { return false },
		Open:  func([]string) error { opened <- struct{}{}; return nil },
	}
	now := time.Now()
	finished := make(chan struct{})
	res, err := e.query(ctx, func() core.Result {
		if _, _, baselineErr := e.observeAppRestart("", "app:old", now, nil); baselineErr != nil {
			return core.Result{"error": baselineErr}
		}
		before := e.snapshotAppRestart(now)
		plans, interval, observeErr := e.observeAppRestart("app:old", "app:new", now, before)
		if observeErr == nil {
			go func() { e.deliverAppRestart(ctx, "app:new", plans, interval); close(finished) }()
		}
		return core.Result{"error": observeErr, "plans": len(plans)}
	})
	if err != nil || res["error"] != nil || res["plans"] != 1 {
		t.Fatalf("setup restart did not apply: %v %v", res, err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("restart adapter did not finish")
	}
	if len(queued) != 1 || len(opened) != 1 || len(s.Inputs()) != 0 {
		t.Fatalf("restart stranded or duplicated cold wake: queue=%d opens=%d inputs=%d", len(queued), len(opened), len(s.Inputs()))
	}
}
