// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) prepareMailboxCapacity(op *core.Op, now time.Time) error {
	if op.Kind != core.OpSendMessage {
		return nil
	}
	op.MailboxCapacity = true
	err := e.state.Admit(op)
	if errors.Is(err, core.ErrMailboxFull) {
		e.escalateMailboxFull(op.To, now)
	}
	return err
}

// A capacity refusal has no delivered envelope of its own. Reference the oldest
// blocking ask; copy neither the refused body nor caller-supplied alert text.
func (e *Engine) escalateMailboxFull(recipient string, now time.Time) {
	var source *core.Message
	for _, m := range e.state.Messages {
		if m.To != recipient || m.Type == core.MsgNotify || m.Terminal() && !m.DurableDebt() {
			continue
		}
		if !e.mailBelongsTo(m, e.state.Agents[recipient]) {
			continue
		}
		if source == nil || m.Serial < source.Serial {
			source = m
		}
	}
	if source == nil {
		return
	}
	previousContact := source.ContactEscalatedAt
	op := &core.Op{Kind: core.OpContactEscalate, MsgSerial: source.Serial, ContactMailboxFull: true}
	if err := e.state.Admit(op); err != nil {
		slog.Error("could not admit mailbox contact alert", "err", err)
		return
	}
	if _, err := e.applyAndLedger(op, now); err != nil {
		slog.Error("could not record mailbox contact alert", "err", err)
		return
	}
	if previousContact != 0 && previousContact != source.ContactEscalatedAt {
		e.resolveContactIfClear(previousContact, now)
	}
}
