// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

const (
	sendResponseBudget      = 5 * time.Second
	bridgeObservationBudget = 500 * time.Millisecond
)

func isSendCall(req *rpcRequest) bool {
	var call struct {
		Name string `json:"name"`
	}
	return req.Method == "tools/call" && json.Unmarshal(req.Params, &call) == nil && call.Name == "send"
}

type sendRPCReply struct {
	result any
	err    *rpcError
}

// Send observation belongs inside its response budget; all other RPCs retain
// their original observation point, including subscriptions and notifications.
func (s *Server) observeRequest(ctx context.Context, req *rpcRequest) {
	if !isSendCall(req) {
		s.observeBridge(ctx, req.Params)
	}
}

func (s *Server) dispatchWithSendBudget(
	ctx context.Context, req *rpcRequest, bearerToken, nonce string, ui bool, client *clientInfoJSON,
) (any, *rpcError) {
	if isSendCall(req) {
		return s.dispatchSend(ctx, req, bearerToken, nonce, ui, client)
	}
	return s.dispatch(ctx, req, bearerToken, nonce, ui, client)
}

// The budget includes bridge observation, session adoption, the writer,
// human presentation and task projection. A context deadline alone cannot
// interrupt a filesystem probe or another blocking implementation port, so
// none of that work owns the HTTP writer. Already-enqueued mutations finish
// on the single writer; the bounded reply never pretends to cancel a commit.
func (s *Server) dispatchSend(
	ctx context.Context, req *rpcRequest, bearerToken, nonce string, ui bool, client *clientInfoJSON,
) (any, *rpcError) {
	started := time.Now()
	var attempt *engine.SendAttempt
	slog.Debug("send stage", "stage", "response", "event", "begin")
	defer func() {
		elapsed := time.Since(started)
		slog.Debug("send stage", "stage", "response", "event", "complete", "elapsed", elapsed)
		if elapsed >= time.Second {
			timings := attempt.StageDurations()
			// Only slow sends produce an INFO line. Durations and a fixed op
			// kind diagnose the stage without logging names, mail, paths or ids.
			slog.Info("slow send", "kind", "send", "elapsed", elapsed,
				"bridge_observation", timings["bridge_observation"],
				"enqueue_wait", timings["enqueue_wait"],
				"writer_apply", timings["writer_apply"],
				"fsync", timings["fsync"],
				"receipt", timings["receipt"],
				"advisories", timings["advisories"])
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, sendResponseBudget)
	defer cancel()
	var receipts <-chan core.Result
	ctx, receipts, attempt = engine.WithSendReceipt(ctx)
	attempt.StartStage("receipt") // duration until durable acceptance, or still pending at the budget
	done := make(chan sendRPCReply, 1)
	go func() {
		// Bridge process discovery is advisory. Its process scan or reconnect
		// receipt must not consume the send's entire response budget.
		observing := attempt.StartStage("bridge_observation")
		observeCtx, stop := context.WithTimeout(ctx, bridgeObservationBudget)
		observed := make(chan struct{})
		go func() {
			s.observeBridge(observeCtx, req.Params)
			close(observed)
		}()
		select {
		case <-observed:
		case <-observeCtx.Done():
		}
		stop()
		observing()
		result, err := s.dispatch(ctx, req, bearerToken, nonce, ui, client)
		done <- sendRPCReply{result, err}
	}()
	select {
	case reply := <-done:
		if ctx.Err() == nil {
			return reply.result, reply.err
		}
	case <-ctx.Done():
	}
	return boundedSendResult(req.Params, receipts, attempt), nil
}

func boundedSendResult(
	params json.RawMessage, receipts <-chan core.Result, attempt *engine.SendAttempt,
) map[string]any {
	select {
	case receipt := <-receipts:
		receipt["advisories"] = "Acceptance is durable; delivery advisories were unavailable within the response budget. " +
			"This receipt does not confirm a wake or recipient visibility. Read read_mail(msg_serial) for later receipts."
		return sendTextResult(receipt, false)
	default:
	}
	var call struct {
		Arguments struct {
			OpID string `json:"op_id"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(params, &call)
	if attempt.AbandonBeforeWriter() {
		return sendTextResult(core.Result{
			"code": "E_SEND_NOT_SENT", "op_id": call.Arguments.OpID,
			"message": "The send timed out before writer admission; no message was sent.",
			"hint": "Resend the payload. Supply an op_id so any later uncertain outcome " +
				"can be retried safely with the same id and payload.",
		}, true)
	}
	hint := "The send may or may not have been accepted. Retry with the exact same op_id and the same payload " +
		"to retrieve its original receipt without duplicating it, within the existing dedup window " +
		"(24 hours or 256 identified ops, whichever is less)."
	if call.Arguments.OpID == "" {
		hint = "The send may or may not have been accepted. No op_id was supplied; safe deduplicated retry is unavailable. " +
			"Check the board and recipient mailbox before resending; a new op_id cannot identify this earlier send."
	}
	return sendTextResult(core.Result{
		"code": "E_SEND_OUTCOME_UNKNOWN", "op_id": call.Arguments.OpID,
		"message": "The send response budget elapsed before an accepted outcome was known.", "hint": hint,
	}, true)
}

func sendTextResult(payload core.Result, isError bool) map[string]any {
	text, _ := json.Marshal(payload) // fixed scalar fields, never user-controlled shapes
	return map[string]any{"isError": isError, "content": []map[string]any{{"type": "text", "text": string(text)}}}
}
