package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// GrantRole sets an agent's role. It is reachable only from the daemon's admin
// path (local secret + admin password), which is why it takes no agent token:
// promotion is a human decision, never an agent's.
func (e *Engine) GrantRole(ctx context.Context, agent, role string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: agent, Mode: role})
}

// Broadcast sends one message to every other live agent. Coordinator-only.
//
// It is deliberately N ordinary sends rather than one clever op: each message
// keeps its own serial, its own ledger entry, its own mailbox accounting and
// deadline. Replay stays exact and a broadcast is indistinguishable from the
// coordinator having written to each agent by hand, which is what it is.
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
		for _, to := range e.state.LiveAgentsExcept(l.ID) {
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
		out["warning"] = "some agents did not receive this: see undelivered"
	}
	return out, nil
}

// AllMail returns every message, decrypted: the god view, for an agent the human
// promoted to admin. It is the one place an agent may read mail it is not party
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

// AgentIdentity is an opaque, stable fingerprint of the agent's own credential,
// or "" when it has none.
//
// A FINGERPRINT, never the nonce. The caller that needs this is the standing
// role reconciler, which has to answer "is the agent holding this name still the
// one I granted the role to", and answering it must not require handing the
// agent's only durable secret to anything outside the engine.
//
// An agent with no nonce has no durable identity at all: it cannot prove it is
// itself after a restart, and a standing role is precisely a thing that outlives
// restarts. The empty string says so, and the reconciler refuses rather than
// pinning nothing.
func (e *Engine) AgentIdentity(ctx context.Context, id string) (string, error) {
	res, err := e.query(ctx, func() core.Result {
		l, ok := e.state.Agents[id]
		if !ok {
			return core.Result{"missing": true}
		}
		if l.Nonce == "" {
			return core.Result{"fingerprint": ""}
		}
		sum := sha256.Sum256([]byte("dibs-role-pin\x00" + l.Nonce))
		return core.Result{"fingerprint": hex.EncodeToString(sum[:])}
	})
	if err != nil {
		return "", err
	}
	if missing, _ := res["missing"].(bool); missing {
		return "", fmt.Errorf("no agent %q", id)
	}
	fp, _ := res["fingerprint"].(string)
	return fp, nil
}
