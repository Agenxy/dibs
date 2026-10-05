package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type sendReceiptKey struct{}

// WithSendReceipt lets a bounded transport retain the acceptance receipt even
// when presentation or advisory work has not finished. Only the original
// writer publishes it, after persistence returned successfully. It carries
// no live state, and receiving it says nothing about recipient delivery.
func WithSendReceipt(ctx context.Context) (context.Context, <-chan core.Result) {
	receipt := make(chan core.Result, 1)
	return context.WithValue(ctx, sendReceiptKey{}, receipt), receipt
}

func sendReceiptFrom(ctx context.Context) chan core.Result {
	receipt, _ := ctx.Value(sendReceiptKey{}).(chan core.Result)
	return receipt
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
		serial, _ := res["msg_serial"].(uint64)
		if note := e.sendDeliveryNote(e.state.Agents[op.To], e.state.Messages[serial], now); note != "" {
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
