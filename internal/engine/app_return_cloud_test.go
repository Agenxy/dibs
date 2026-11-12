// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// The return pass snapshots eligible agents before probing ownership. A
// registration update while that probe runs must cancel its stale navigation.
func TestRestartReturnRechecksCloudSurfaceBeforeNavigation(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.Start(t, "no-owner", nil)
	previousEpoch, previousOwned, previousShower := appRestartEpoch.Load(), appThreadOwned, shower
	appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
	var opens atomic.Int32
	shower = &harnessenv.Shower{
		Ownership: func(string) harnessenv.ThreadOwnership { return harnessenv.ThreadOwnership{} },
		Open:      func([]string) error { opens.Add(1); return nil },
	}
	t.Cleanup(func() {
		appRestartEpoch.Store(previousEpoch)
		appThreadOwned, shower = previousOwned, previousShower
	})
	e, ctx, worker, sender := nativeAppEngine(t)
	e.appSettled.Store("app:A")
	nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	var once sync.Once
	appThreadOwned = func(ctx context.Context, thread string) (bool, error) {
		once.Do(func() {
			res, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: worker,
				Agent: &core.AgentInfo{Surface: harnessenv.ChatGPTCloudExec}})
			if err != nil || res["ok"] != true {
				t.Fatalf("setup: cloud surface update failed: %v %v", res, err)
			}
		})
		return codexipc.Owned(ctx, thread)
	}
	e.afterAppReturn(ctx, "app:B", nil, 0)
	waitWakeDone(t, e, "worker")
	if opens.Load() != 0 || len(app.Inputs()) != 0 {
		t.Fatalf("reload navigated or sent native input after cloud reclassification: opens=%d inputs=%d", opens.Load(), len(app.Inputs()))
	}
	if _, threads := e.appAgentThreads(ctx); len(threads) != 0 {
		t.Fatalf("explicit cloud surface was included in the next reload: %v", threads)
	}
}
