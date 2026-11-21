// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// DibsNonce is reserved for the daemon's reporting identity, never an agent.
const DibsNonce = "system:dibs"

// OpFinishedWakeNotified sends a notice and marks the original envelope.
// Readers without the kind refuse the ledger instead of losing the mark.
const OpFinishedWakeNotified = "finished_wake_notified"

// UnconsumedEnvelope includes mail the harness has announced without the
// recipient consuming it. Answered or accepted work is outside this state.
func (m *Message) UnconsumedEnvelope() bool {
	return m != nil && !m.Consumed && (m.State == MsgStatePending || m.State == MsgStateDelivered)
}

func (s *State) admitFinishedWakeNotice(op *Op) error {
	if op.Kind != OpFinishedWakeNotified {
		return nil
	}
	actor := s.AgentByToken(op.Token)
	if actor == nil || actor.Nonce != DibsNonce {
		return errf("E_FORBIDDEN", "only the daemon records its own wake outcomes", "finished wake notice is daemon-authored")
	}
	m := s.Messages[op.MsgSerial]
	if !m.UnconsumedEnvelope() || m.From != op.To ||
		op.MsgType != MsgNotify || op.Body == "" || len(op.Body) > s.Limits.MaxBodyBytes {
		return errf("E_BAD_ARG", "notify the original sender of still-unread mail", "invalid finished wake notice")
	}
	return nil
}

// SendsMessage includes the daemon's atomic notice operations. Derived
// history and human delivery must recognize the same birth as the fold does.
func SendsMessage(kind string) bool {
	return kind == OpSendMessage || kind == OpStallNotified || kind == OpFinishedWakeNotified
}

func (s *State) applyFinishedWakeNotified(actor *Agent, op *Op, now time.Time) (Result, []Event, error) {
	source := s.Messages[op.MsgSerial]
	if source == nil {
		return nil, nil, errf("E_NO_MESSAGE", "inspect retained mail before reporting a wake outcome",
			"message %d is gone", op.MsgSerial)
	}
	if source.FinishedWakeNotice != 0 {
		return Result{"ok": true, "msg_serial": source.FinishedWakeNotice, "deduplicated": true}, nil, nil
	}
	result, events, err := s.applySend(actor, &Op{
		To: op.To, MsgType: MsgNotify, Body: op.Body, MailboxCapacity: op.MailboxCapacity,
	}, now)
	if err == nil {
		source.FinishedWakeNotice, _ = result["msg_serial"].(uint64)
	}
	return result, events, err
}

func (s *State) admitStallNotice(op *Op) error {
	if op.Kind != OpStallNotified {
		return nil
	}
	actor := s.AgentByToken(op.Token)
	if actor == nil || actor.Nonce != DibsNonce {
		return errf("E_FORBIDDEN", "only the daemon records its own stall notices", "stall_notified is daemon-authored")
	}
	m := s.Messages[op.MsgSerial]
	if m == nil || m.Type != MsgRequest || m.State != MsgStateApproved || m.From != op.To {
		return errf("E_BAD_REQUEST", "report only a still-approved request to its sender",
			"request %d is not owed", op.MsgSerial)
	}
	if op.DeclarationSerial == 0 || op.DeclarationSerial > s.Serial ||
		op.Body == "" || len(op.Body) > s.Limits.MaxBodyBytes {
		return errf("E_BAD_REQUEST", "record the current declaration version and a bounded notice", "invalid stall notice")
	}
	return nil
}

// The notice and its suppression watermark are ONE transition. A full mailbox
// or another send failure leaves the watermark untouched; replay cannot retain
// an "already told" fact without the actual notice it records.
func (s *State) applyStallNotified(actor *Agent, op *Op, now time.Time) (Result, []Event, error) {
	m := s.Messages[op.MsgSerial]
	if m == nil {
		return nil, nil, errf("E_NO_MESSAGE", "inspect the open requests before reporting a stall",
			"request %d is gone", op.MsgSerial)
	}
	if m.StallNotifiedDeclaration == op.DeclarationSerial {
		return Result{"ok": true, "deduplicated": true}, nil, nil
	}
	r, events, err := s.applySend(actor, &Op{To: op.To, MsgType: MsgNotify, Body: op.Body}, now)
	if err == nil {
		m.StallNotifiedDeclaration = op.DeclarationSerial
	}
	return r, events, err
}
