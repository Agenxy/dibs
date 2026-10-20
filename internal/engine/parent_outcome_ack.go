// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A parent serial is the bounded, body-free alternative to reading every
// report. Reuse the explicit read op: an acknowledgement here neither consumes
// the envelope nor accepts work, and historical ack operations fold unchanged.
func (e *Engine) handleParentOutcomeAck(op *core.Op, actor *core.Agent, now time.Time) (core.Result, error, bool) {
	if actor == nil || op.Kind != core.OpAckMessage {
		return nil, nil, false
	}
	m := e.state.Messages[op.MsgSerial]
	sender, handled := parentOutcomeAckTarget(actor, m)
	if !handled {
		return nil, nil, false
	}
	if allowed, _ := core.MessageAccess(op.MsgSerial, m, actor); !allowed {
		return nil, core.ErrNoMessage(op.MsgSerial, actor.TruncatedBefore), true
	}
	if !m.Terminal() {
		return nil, &core.Error{
			Code: "E_NOT_TERMINAL", Msg: "this sent message has no outcome to acknowledge yet",
			Hint: "wait for a response; read_mail can still show the original message",
		}, true
	}
	through, field := m.LatestOutcomeSerial(), "outcome_read_serial"
	if !sender {
		through, field = m.LatestReviewSerial(), "review_read_serial"
	}
	if through != 0 {
		if _, err := e.applyAndLedger(&core.Op{
			Kind: core.OpOutcomeRead, Token: op.Token,
			MsgSerial: m.Serial, OutcomeThroughSerial: through,
		}, now); err != nil {
			return nil, err, true
		}
	}
	e.clearNoticesFor(actor.ID, m.Serial)
	e.seen[actor.ID] = now
	e.noteAuthenticatedContact(actor, now)
	e.confirmSocketOffer(actor, now)
	read := m.OutcomeReadAt
	if !sender {
		read = m.ReviewReadAt
	}
	return core.Result{"ok": true, "state": m.State, "msg_serial": m.Serial, field: read}, nil, true
}

func parentOutcomeAckTarget(actor *core.Agent, m *core.Message) (sender, handled bool) {
	if m == nil || (m.Type != core.MsgRequest && m.Type != core.MsgQuestion) {
		return false, false
	}
	if m.From == m.To && !m.Terminal() {
		return false, false // a self-addressed incoming ask can still be acknowledged
	}
	sender = m.From == actor.ID
	reviews := m.To == actor.ID && m.Type == core.MsgRequest && m.Terminal()
	return sender, sender || reviews
}
