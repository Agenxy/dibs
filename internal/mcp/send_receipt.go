package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

const sendResponseBudget = 5 * time.Second

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

// The budget includes bridge observation, session adoption, the writer,
// human presentation and task projection. A context deadline alone cannot
// interrupt a filesystem probe or another blocking implementation port, so
// none of that work owns the HTTP writer. Already-enqueued mutations finish
// on the single writer; the bounded reply never pretends to cancel a commit.
func (s *Server) dispatchSend(
	ctx context.Context, req *rpcRequest, bearerToken, nonce string, ui bool, client *clientInfoJSON,
) (any, *rpcError) {
	started := time.Now()
	slog.Debug("send stage", "stage", "response", "event", "begin")
	defer func() {
		slog.Debug("send stage", "stage", "response", "event", "complete", "elapsed", time.Since(started))
	}()
	ctx, cancel := context.WithTimeout(ctx, sendResponseBudget)
	defer cancel()
	ctx, receipts := engine.WithSendReceipt(ctx)
	done := make(chan sendRPCReply, 1)
	go func() {
		s.observeBridge(ctx, req.Params)
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
	return boundedSendResult(req.Params, receipts), nil
}

func boundedSendResult(params json.RawMessage, receipts <-chan core.Result) map[string]any {
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
