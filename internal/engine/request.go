// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The caller owns its original stage hold. A separate request hold bridges
// cancellation AFTER enqueue: only the writer then knows whether registration
// committed. Its completion hands protection to the snapshot hold, if needed.
func (e *Engine) holdRegistration(req *request, blob string) {
	if e.blobs != nil {
		e.blobs.Hold(blob)
		req.finish = func() { e.blobs.Release(blob) }
	}
}

func (req request) complete() {
	if req.finish != nil {
		req.finish()
	}
}

// Own cleanup from the moment the unbuffered submission is received, including
// invitation refusal before fn runs and fail-stop panics. send may already have
// returned cancellation; the buffered reply must not require a waiting caller.
func (e *Engine) serveRequest(req request) {
	defer req.complete()
	if !req.attempt.admitToWriter() {
		req.reply <- reply{nil, context.Canceled}
		return
	}
	if err := e.admitInvitation(req.invite, req.op); err != nil {
		req.reply <- reply{nil, err}
		return
	}
	if req.fn != nil {
		res := req.fn()
		if errv, ok := res["error"].(error); ok {
			req.reply <- reply{nil, errv}
		} else {
			req.reply <- reply{res, nil}
		}
		return
	}
	first := e.firstCheckInFor(req)
	res, err := e.execWithReceipt(req.op, time.Now(), req.receipt, req.attempt, req.prepaid, req.capacity, req.process)
	if err == nil {
		if first {
			req.firstCheckIn.Store(true)
		}
		advisories := req.attempt.StartStage("advisories")
		e.sendAdvisories(req.op, res, time.Now())
		advisories()
	}
	req.reply <- reply{res, err}
}

// At ingress to the single writer the previous awareness checkpoint is still
// intact. Authentication in exec is authoritative. A refused call never
// publishes the receipt, and a liveness wake does not reset the gate.
func (e *Engine) firstCheckInFor(req request) bool {
	if req.firstCheckIn == nil || req.op.Kind != core.OpAckBoard {
		return false
	}
	actor := e.state.AgentByToken(req.op.Token)
	return actor != nil && actor.AckedSerial == 0
}

// Keep the query's result/error convention for registration closures without
// letting a canceled caller release the writer's pending byte protection.
func (e *Engine) registrationRequest(blob string, fn func() core.Result) request {
	req := request{fn: fn, reply: make(chan reply, 1)}
	e.holdRegistration(&req, blob)
	return req
}
