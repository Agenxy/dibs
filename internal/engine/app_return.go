// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// The app's own owner discovery, replaced by tests: a Go test never reaches
// the operator's app.
var appThreadOwned = codexipc.Owned

// appReturnReady bounds the wait for a relaunched app to accept IPC
// connections. Measured 2026-10-10: native dials were refused for about five
// seconds of a ChatGPT restart.
var appReturnReady = 30 * time.Second

// afterAppReturn waits for IPC, settles the current epoch and re-offers work
// through the ordinary native-first, then queue, then wake route. A wake URL
// cannot preload an empty queue, so there is no separate loading pass.
func (e *Engine) afterAppReturn(ctx context.Context, epoch string, plans []wakePlan, interval time.Duration) {
	agents, threads := e.appAgentThreads(ctx)
	if len(threads) > 0 {
		_ = e.waitAppIPC(ctx, epoch, threads[0])
	}
	// Settled describes the return handshake, never ownership or delivery.
	// A late handshake cannot settle a different app incarnation.
	if current, known := currentAppRestartEpoch(); known && current == epoch {
		e.appSettled.Store(epoch)
	}
	if len(plans) > 0 {
		e.deliverAppRestart(ctx, epoch, plans, interval)
	}
	_, _ = e.query(ctx, func() core.Result {
		now := time.Now()
		for _, agent := range agents {
			if len(e.deliveryKeysAt(agent, now)) > 0 {
				e.retryWakeDecision(agent)
			}
		}
		return nil
	})
}

// All local native-route agents are reconsidered after return, including an
// archived row whose thread is already owned. Navigation is narrower: only
// non-archived agents with original delivery still owed provide an IPC probe target.
func (e *Engine) appAgentThreads(ctx context.Context) (agents, threads []string) {
	_, _ = e.query(ctx, func() core.Result {
		ids := make([]string, 0, len(e.state.Agents))
		for id := range e.state.Agents {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		seen := map[string]bool{}
		for _, id := range ids {
			l := e.state.Agents[id]
			if l == nil || l.Retired() || surfaceOf(l) != harnessenv.ChatGPTApp || e.remoteHostOf(l) != "" ||
				!e.nativeAppRoute(l) {
				continue
			}
			thread := threadIDOf(l)
			if thread == "" || e.bindingNeedsRecovery(l) {
				continue
			}
			agents = append(agents, id)
			if l.Status == core.StatusArchived {
				e.noteAppBinding(l, "archived")
			}
			if l.Gone() || len(e.freshCommandKeys(id, time.Now())) == 0 || seen[thread] {
				continue
			}
			seen[thread] = true
			threads = append(threads, thread)
		}
		return nil
	})
	return agents, threads
}

// waitAppIPC waits for the relaunched app to accept connections. A socket
// that refuses or is not there yet is the app still starting; any other
// failure, or a later incarnation, ends the wait without loading anything.
func (e *Engine) waitAppIPC(ctx context.Context, epoch, probe string) bool {
	deadline := time.Now().Add(appReturnReady)
	for {
		_, err := appThreadOwned(ctx, probe)
		if err == nil {
			return true
		}
		starting := errors.Is(err, codexipc.ErrAppNotListening) || errors.Is(err, codexipc.ErrNoSocket)
		if !starting || !time.Now().Before(deadline) {
			slog.Warn("the ChatGPT app did not accept IPC connections after it restarted; ordinary delivery remains unconfirmed",
				"err", err, "hint", "mail retains its ordinary background route")
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
		if current, known := currentAppRestartEpoch(); !known || current != epoch {
			return false // a later incarnation runs its own pass
		}
	}
}

// appReturnPending reports a running app incarnation whose IPC-return handshake is due or under way.
func (e *Engine) appReturnPending() bool {
	settled, _ := e.appSettled.Load().(string)
	current, known := currentAppRestartEpoch()
	return settled != "" && known && current != "absent" && current != settled
}
