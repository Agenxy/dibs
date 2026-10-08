// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// DibsNonce is reserved for the daemon's reporting identity, never an agent.
const DibsNonce = "system:dibs"

// SendsMessage includes the daemon's atomic stall-notice operation. Derived
// history and human delivery must recognize the same birth as the fold does.
func SendsMessage(kind string) bool { return kind == OpSendMessage || kind == OpStallNotified }

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
