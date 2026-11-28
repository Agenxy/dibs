// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// DibsNonce is reserved for the daemon's reporting identity, never an agent.
const DibsNonce = "system:dibs"

// OpFinishedWakeNotified sends a notice and marks the original envelope.
// Readers without the kind refuse the ledger instead of losing the mark.
const OpFinishedWakeNotified = "finished_wake_notified"

// OpFinishedWakeBatchNotified records one notice for several sources. Older
// readers must refuse the new kind rather than silently lose source markers.
const OpFinishedWakeBatchNotified = "finished_wake_batch_notified"

// UnconsumedEnvelope includes mail the harness has announced without the
// recipient consuming it. Answered or accepted work is outside this state.
func (m *Message) UnconsumedEnvelope() bool {
	return m != nil && !m.Consumed && (m.State == MsgStatePending || m.State == MsgStateDelivered)
}

func (s *State) admitFinishedWakeNotice(op *Op) error {
	if !finishedWakeNotice(op.Kind) {
		return nil
	}
	actor := s.AgentByToken(op.Token)
	if actor == nil || actor.Nonce != DibsNonce {
		return errf("E_FORBIDDEN", "only the daemon records its own wake outcomes", "finished wake notice is daemon-authored")
	}
	if op.MsgType != MsgNotify || op.Body == "" || len(op.Body) > s.Limits.MaxBodyBytes {
		return errf("E_BAD_ARG", "notify the original sender of still-unread mail", "invalid finished wake notice")
	}
	if !s.finishedWakeSourcesMatch(op.To, finishedWakeSources(op)) {
		return errf("E_BAD_ARG", "list still-unread source serials in order for one sender and one recipient",
			"invalid finished wake notice sources")
	}
	return nil
}

func (s *State) finishedWakeSourcesMatch(sender string, serials []uint64) bool {
	if len(serials) == 0 {
		return false
	}
	first := s.Messages[serials[0]]
	if !first.UnconsumedEnvelope() || first.From != sender {
		return false
	}
	for i, serial := range serials[1:] {
		m := s.Messages[serial]
		if !m.UnconsumedEnvelope() || m.From != sender || m.To != first.To ||
			m.FinishedWakeNotice != first.FinishedWakeNotice || serials[i] >= serial {
			return false
		}
	}
	return true
}

func finishedWakeNotice(kind string) bool {
	return kind == OpFinishedWakeNotified || kind == OpFinishedWakeBatchNotified
}

func finishedWakeSources(op *Op) []uint64 {
	if op.Kind == OpFinishedWakeNotified {
		return []uint64{op.MsgSerial}
	}
	return op.MsgSerials
}

// SendsMessage includes the daemon's atomic notice operations. Derived
// history and human delivery must recognize the same birth as the fold does.
func SendsMessage(kind string) bool {
	return kind == OpSendMessage || kind == OpStallNotified || kind == OpStalledWorkNotified || finishedWakeNotice(kind)
}

func (s *State) applyFinishedWakeNotified(actor *Agent, op *Op, now time.Time) (Result, []Event, error) {
	serials := finishedWakeSources(op)
	if len(serials) == 0 {
		return nil, nil, errf("E_BAD_ARG", "list the sources of the wake notice", "finished wake notice has no sources")
	}
	for _, serial := range serials {
		source := s.Messages[serial]
		if source == nil {
			return nil, nil, errf("E_NO_MESSAGE", "inspect retained mail before reporting a wake outcome",
				"message %d is gone", serial)
		}
		if source.FinishedWakeNotice != 0 {
			return Result{"ok": true, "msg_serial": source.FinishedWakeNotice, "deduplicated": true}, nil, nil
		}
	}
	result, events, err := s.applySend(actor, &Op{
		To: op.To, MsgType: MsgNotify, Body: op.Body, MailboxCapacity: op.MailboxCapacity,
	}, now)
	if err == nil {
		receipt, _ := result["msg_serial"].(uint64)
		for _, serial := range serials {
			s.Messages[serial].FinishedWakeNotice = receipt
		}
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
