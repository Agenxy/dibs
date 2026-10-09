// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A finite mail-arrival batch, not an elapsed-work or successful-write timer.
const wakeBurstWindow = 200 * time.Millisecond

type wakeBurst struct {
	event core.Event
	timer *time.Timer
}

func (e *Engine) coalesceEventWake(ev core.Event) {
	if e.state == nil || e.ops == nil {
		return // a decision-only fixture has no writer loop to run a batch
	}
	l := e.state.Agents[ev.To]
	if l == nil || !e.wakesFor(ev, l) {
		return
	}
	if e.wakeBursts == nil {
		e.wakeBursts = map[string]*wakeBurst{}
	}
	if batch := e.wakeBursts[l.ID]; batch != nil {
		batch.event = ev
		return // fixed from the first arrival; a stream cannot postpone it
	}
	batch := &wakeBurst{event: ev}
	agent := l.ID
	e.wakeBursts[agent] = batch
	batch.timer = time.AfterFunc(wakeBurstWindow, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = e.query(ctx, func() core.Result {
			if e.wakeBursts[agent] == batch {
				delete(e.wakeBursts, agent)
				e.maybeWake(batch.event)
			}
			return nil
		})
	})
}
