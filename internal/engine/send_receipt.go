// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type sendReceiptKey struct{}

type sendAttemptKey struct{}

// SendAttempt records only timings and the writer admission boundary. The
// deadline and writer decide admission under one lock: either the writer owns
// the request, or the deadline closes the gate and the writer must discard it.
type SendAttempt struct {
	mu        sync.Mutex
	admitted  bool
	abandoned bool
	started   map[string]time.Time
	durations map[string]time.Duration
}

func (a *SendAttempt) maySubmit() bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.abandoned
}

// admitToWriter is the writer's first act after receiving a request. It makes
// ownership atomic with the deadline's abandonment decision.
func (a *SendAttempt) admitToWriter() bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.abandoned {
		return false
	}
	a.admitted = true
	return true
}

// AbandonBeforeWriter is the only negative receipt the bounded transport can
// prove. If the writer got there first, the result remains uncertain until a
// durable receipt arrives.
func (a *SendAttempt) AbandonBeforeWriter() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admitted {
		return false
	}
	a.abandoned = true
	return true
}

// StartStage records a privacy-safe duration for a slow-send diagnostic. The
// caller may snapshot an in-progress stage at its response deadline.
func (a *SendAttempt) StartStage(stage string) func() {
	if a == nil {
		return func() {}
	}
	a.mu.Lock()
	if a.started == nil {
		a.started = make(map[string]time.Time)
	}
	a.started[stage] = time.Now()
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.StopStage(stage)
		})
	}
}

// StopStage may be called by the writer after the HTTP goroutine started the
// receipt clock. It is harmless when admission failed before any receipt.
func (a *SendAttempt) StopStage(stage string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if started, ok := a.started[stage]; ok {
		if a.durations == nil {
			a.durations = make(map[string]time.Duration)
		}
		a.durations[stage] += time.Since(started)
		delete(a.started, stage)
	}
}

// StageDurations copies completed and active timings without exposing request
// content, identities, paths or op ids.
func (a *SendAttempt) StageDurations() map[string]time.Duration {
	result := make(map[string]time.Duration)
	if a == nil {
		return result
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for stage, duration := range a.durations {
		result[stage] = duration
	}
	for stage, started := range a.started {
		result[stage] += time.Since(started)
	}
	return result
}

// WithSendReceipt lets a bounded transport retain the acceptance receipt even
// when presentation or advisory work has not finished. Only the original
// writer publishes it, after persistence returned successfully. It carries
// no live state, and receiving it says nothing about recipient delivery.
func WithSendReceipt(ctx context.Context) (context.Context, <-chan core.Result, *SendAttempt) {
	receipt := make(chan core.Result, 1)
	attempt := &SendAttempt{}
	ctx = context.WithValue(ctx, sendReceiptKey{}, receipt)
	return context.WithValue(ctx, sendAttemptKey{}, attempt), receipt, attempt
}

func sendReceiptFrom(ctx context.Context) chan core.Result {
	receipt, _ := ctx.Value(sendReceiptKey{}).(chan core.Result)
	return receipt
}

func sendAttemptFrom(ctx context.Context) *SendAttempt {
	attempt, _ := ctx.Value(sendAttemptKey{}).(*SendAttempt)
	return attempt
}

func captureSendReceipt(receipt chan core.Result, res core.Result) {
	if receipt == nil || res["ok"] != true || res["msg_serial"] == nil {
		return
	}
	// These values are scalars (deadline is a time.Time), not pointers into
	// the writer's maps. In particular do not copy the fold's sleeping note:
	// it is not a measured wake route, and later decorators replace it.
	copy := core.Result{"ok": true, "msg_serial": res["msg_serial"]}
	for _, key := range []string{"deadline", "deduplicated"} {
		if value, ok := res[key]; ok {
			copy[key] = value
		}
	}
	select {
	case receipt <- copy:
	default:
	}
}

// Both notes are derived inside the request that accepted the send, with no
// second authentication or writer submission after its receipt. The caller's
// token and recipient reference have already passed normal ingress.
func (e *Engine) sendAdvisories(op *core.Op, res core.Result, now time.Time) {
	if op.Kind != core.OpSendMessage || res == nil {
		return
	}
	done := beginSendStage(op, "advisories")
	defer done()
	route, _ := res["human_route"].(string)
	if route == "desktop" || route == "relay" {
		delete(res, "note")
	} else {
		if view := e.queueWakeView(e.state.Agents[op.To], now); view != nil {
			res["queue_wake"] = view
		}
		serial, _ := res["msg_serial"].(uint64)
		if note := e.sendDeliveryNote(e.state.Agents[op.To], e.state.Messages[serial]); note != "" {
			res["note"] = note
		}
	}
	if actor := e.state.AgentByToken(op.Token); actor != nil {
		if note := unansweredNote(e.state, actor.ID, op.To, now); note != "" {
			res["unanswered"] = note
		}
	}
}

// Begin as well as completion is logged, so a stuck stage names itself. Debug
// only: no body, token, participant name, op_id or filesystem path is logged.
func beginSendStage(op *core.Op, stage string) func() {
	if op == nil || op.Kind != core.OpSendMessage {
		return func() {}
	}
	started := time.Now()
	slog.Debug("send stage", "stage", stage, "event", "begin")
	var once sync.Once
	return func() {
		once.Do(func() {
			slog.Debug("send stage", "stage", stage, "event", "complete", "elapsed", time.Since(started))
		})
	}
}
