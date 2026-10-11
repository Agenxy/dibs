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

// afterAppReturn runs once per new ChatGPT app incarnation, off the writer.
// Order matters: threads are loaded first, so the restart notices and the
// mail held while the app was down are then delivered natively, into loaded
// threads, and none of them opens anything.
func (e *Engine) afterAppReturn(ctx context.Context, epoch string, plans []wakePlan, interval time.Duration) {
	agents, threads := e.appAgentThreads(ctx)
	if len(threads) > 0 && e.waitAppIPC(ctx, epoch, threads[0]) {
		owned := func(thread string) (bool, error) { return appThreadOwned(ctx, thread) }
		loaded, opened, err := shower.LoadChatGPTThreads(threads, owned, 5*time.Second)
		if err != nil {
			slog.Warn("could not load every agent thread after the ChatGPT app restarted", "err", err,
				"loaded", loaded, "opened", opened, "threads", len(threads),
				"hint", "an agent whose thread is not loaded is opened by its next mail instead")
		} else {
			slog.Info("loaded the agents' threads after the ChatGPT app restarted",
				"loaded", loaded, "opened", opened, "threads", len(threads))
		}
	}
	// Settled before any delivery: from here an unloaded thread is one the
	// load could not reach, and its mail takes the ordinary route.
	e.appSettled.Store(epoch)
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

// appAgentThreads is every local ChatGPT-app agent on the native route and
// its thread, sorted so the order of opens does not depend on map order.
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
			if thread == "" || seen[thread] {
				continue
			}
			seen[thread] = true
			agents, threads = append(agents, id), append(threads, thread)
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
			slog.Warn("the ChatGPT app did not accept IPC connections after it restarted; not loading agent threads",
				"err", err, "hint", "each agent's next mail opens its thread instead")
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

// appReturnPending reports a running app incarnation whose threads have not
// been loaded yet: afterAppReturn is due or under way.
func (e *Engine) appReturnPending() bool {
	settled, _ := e.appSettled.Load().(string)
	current, known := currentAppRestartEpoch()
	return settled != "" && known && current != "absent" && current != settled
}
