// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// Enter at the same ledgered restart observation and dispatcher as the watcher.
// Only the process observation and external app are fixtures.
func TestNativeAppRestartUsesOwnedThreadWithoutQueueOrOpen(t *testing.T) {
	s := testcodexipc.Start(t, "idle", nil)
	e, ctx, _, _ := nativeAppEngine(t)
	e.SetAppRestartDefaults(time.Hour, time.Hour, true, true)
	previous := appRestartEpoch.Load()
	appRestartEpoch.Store(func() (string, bool) { return "app:new", true })
	t.Cleanup(func() { appRestartEpoch.Store(previous) })
	now := time.Now()
	res, err := e.query(ctx, func() core.Result {
		e.wakers.mu.Lock()
		e.wakers.queued = map[string]time.Time{"worker": now}
		e.wakers.mu.Unlock()
		if _, _, baselineErr := e.observeAppRestart("", "app:old", now, nil); baselineErr != nil {
			return core.Result{"error": baselineErr}
		}
		before := e.snapshotAppRestart(now)
		plans, interval, observeErr := e.observeAppRestart("app:old", "app:new", now, before)
		if observeErr == nil {
			go e.deliverAppRestart(ctx, "app:new", plans, interval)
		}
		return core.Result{"error": observeErr, "plans": len(plans)}
	})
	if err != nil || res["error"] != nil || res["plans"] != 1 {
		t.Fatalf("setup restart did not apply: %v %v", res, err)
	}
	awaitNative(t, s, 1)
	calls := s.Inputs()
	p := calls[0]["params"].(map[string]any)["turnStart"].(map[string]any)["request"].(map[string]any)
	if text := p["input"].([]any)[0].(map[string]any)["text"]; text != "Dibs: the ChatGPT app restarted. Your pre-restart declarations are on the board." {
		t.Fatalf("unexpected restart notice: %v", text)
	}
}
