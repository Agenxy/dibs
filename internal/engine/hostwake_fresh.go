// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"

	"github.com/agenxy/dibs/internal/core"
)

// ErrWakeFenceDenied deliberately distinguishes no foreign mailbox state.
var ErrWakeFenceDenied = errors.New("wake fence not available")

func lostHostWakeResult(id uint64, host, detail string, native bool) WakeResult {
	r := WakeResult{ID: id, Host: host, Detail: detail}
	if native {
		r.NoRetry, r.NativeDelivery = true, "unknown"
	}
	return r
}

// HostWakeOwed is a read-only pre-write fence for a native host bridge. It
// neither consumes mail nor confirms delivery. Only the existing pending
// request's host, incarnation, session and captured original items qualify.
func (e *Engine) HostWakeOwed(ctx context.Context, id uint64, host string) (bool, error) {
	e.hostWakes.mu.Lock()
	p, found := e.hostWakes.pending[id]
	e.hostWakes.mu.Unlock()
	if !found || p.host != host {
		return false, ErrWakeFenceDenied
	}
	res, err := e.query(ctx, func() core.Result {
		e.hostWakes.mu.Lock()
		current, found := e.hostWakes.pending[id]
		e.hostWakes.mu.Unlock()
		if !found || current.ch != p.ch {
			return core.Result{"owed": false}
		}
		return core.Result{"owed": e.originalNativeWakeOwed(p.plan, p.plan.agent)}
	})
	owed, _ := res["owed"].(bool)
	return owed, err
}
