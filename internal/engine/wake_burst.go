// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
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
	if e.state == nil || e.ops == nil || e.wakeContext == nil {
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
	ctx := e.wakeContext
	batch.timer = time.AfterFunc(wakeBurstWindow, func() {
		// A live but stalled writer still owes this batch. Only shutdown cancels
		// admission; an elapsed enqueue deadline would strand the batch forever.
		_, _ = e.query(ctx, func() core.Result {
			if e.wakeBursts[agent] == batch {
				delete(e.wakeBursts, agent)
				e.maybeWake(batch.event)
			}
			return nil
		})
	})
}

// On the writer, after serving stops. Waiting callbacks use Run's context and
// are canceled by its cleanup, including when a writer fails closed.
func (e *Engine) stopWakeBursts() {
	for _, burst := range e.wakeBursts {
		burst.timer.Stop()
	}
	clear(e.wakeBursts)
}
