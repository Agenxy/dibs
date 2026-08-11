package engine

import (
	"context"
	"time"

	"github.com/agenxy/lanes/internal/core"
)

// GrantRole sets a lane's role. It is reachable only from the daemon's admin
// path (local secret + admin password), which is why it takes no lane token:
// promotion is a human decision, never an agent's.
func (e *Engine) GrantRole(ctx context.Context, lane, role string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: lane, Mode: role})
}

// Broadcast sends one message to every other live lane. Coordinator-only.
//
// It is deliberately N ordinary sends rather than one clever op: each message
// keeps its own serial, its own ledger entry, its own mailbox accounting and
// deadline. Replay stays exact and a broadcast is indistinguishable from the
// coordinator having written to each lane by hand, which is what it is.
func (e *Engine) Broadcast(ctx context.Context, token, msgType, body string) (core.Result, error) {
	// Resolve the sender and the recipient set inside the loop, so the fan-out
	// is taken from one consistent snapshot of the board.
	snap, err := e.query(ctx, func() core.Result {
		l, errRes := e.authRead(token, time.Now())
		if errRes != nil {
			return errRes
		}
		if !l.IsCoordinator() {
			return core.Result{"error": core.ErrNotCoordinator}
		}
		ids := []string{}
		for _, to := range e.state.LiveLanesExcept(l.ID) {
			ids = append(ids, to.ID)
		}
		return core.Result{"ids": ids}
	})
	if err != nil {
		return nil, err
	}
	if e2, ok := snap["error"].(error); ok {
		return nil, e2
	}
	ids, _ := snap["ids"].([]string)

	delivered := []uint64{}
	failed := map[string]string{}
	for _, id := range ids {
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: token, To: id, MsgType: msgType, Body: body,
		})
		if err != nil {
			// Honest partial delivery: say who did not get it and why, rather
			// than reporting a success that did not happen.
			failed[id] = err.Error()
			continue
		}
		if ser, ok := res["msg_serial"].(uint64); ok {
			delivered = append(delivered, ser)
		}
	}
	out := core.Result{"ok": true, "delivered": len(delivered), "msg_serials": delivered}
	if len(failed) > 0 {
		out["undelivered"] = failed
		out["warning"] = "some lanes did not receive this: see undelivered"
	}
	return out, nil
}

// AllMail returns every message, decrypted: the god view, for a lane the human
// promoted to admin. It is the one place a lane may read mail it is not party
// to, which is exactly why it is gated on the admin role and nothing weaker.
func (e *Engine) AllMail(ctx context.Context, token string) (core.Result, error) {
	res, err := e.query(ctx, func() core.Result {
		l, errRes := e.authRead(token, time.Now())
		if errRes != nil {
			return errRes
		}
		if !l.IsAdmin() {
			return core.Result{"error": core.ErrNotAdmin}
		}
		out := make([]*core.Message, 0, len(e.state.Messages))
		for _, m := range e.state.Messages {
			out = append(out, m)
		}
		return core.Result{"messages": out, "serial": e.state.Serial}
	})
	if err != nil {
		return nil, err
	}
	if e2, ok := res["error"].(error); ok {
		return nil, e2
	}
	return res, nil
}
