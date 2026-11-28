// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// OpStalledWorkObserved holds a live observation until a coordinator can be told.
const OpStalledWorkObserved = "stalled_work_observed"

// OpStalledWorkNotified commits the coordinator mail and its work watermark
// together. Older readers refuse this kind instead of dropping the watermark.
const OpStalledWorkNotified = "stalled_work_notified"

// StalledWorkNotice records the newest work version already brought to a
// coordinator's attention. Mail retention must not reopen that episode.
type StalledWorkNotice struct {
	DeclarationSerial uint64    `json:"declaration_serial"`
	NoticeSerial      uint64    `json:"notice_serial,omitempty"`
	Session           string    `json:"session"`
	Host              string    `json:"host,omitempty"`
	ObservedAt        time.Time `json:"observed_at"`
}

func (s *State) admitStalledWorkNotice(op *Op) error {
	if op.Kind != OpStalledWorkNotified && op.Kind != OpStalledWorkObserved {
		return nil
	}
	worker := s.Agents[op.StalledWorker]
	if worker == nil || worker.Retired() || op.SessionID == "" || !worker.SessionIsCurrent(op.SessionID) ||
		op.DeclarationSerial <= worker.CreatedSerial || op.DeclarationSerial > s.Serial {
		return errf("E_BAD_STALL_NOTICE", "record the current worker, session and work version",
			"invalid stalled work evidence")
	}
	if op.Kind == OpStalledWorkObserved {
		return nil // tokenless writer event, excluded from public ingress
	}
	return s.admitStalledWorkDelivery(op)
}

func (s *State) admitStalledWorkDelivery(op *Op) error {
	actor, recipient := s.AgentByToken(op.Token), s.Agents[op.To]
	if actor == nil || actor.Nonce != DibsNonce {
		return errf("E_FORBIDDEN", "read the coordinator's stall notice", "stalled work observations are daemon-authored")
	}
	if recipient == nil || recipient.Retired() || !recipient.IsCoordinator() || op.To == op.StalledWorker {
		return errf("E_BAD_STALL_NOTICE", "select another registered coordinator", "invalid stalled work recipient")
	}
	if op.Body == "" || len(op.Body) > s.Limits.MaxBodyBytes {
		return errf("E_BAD_STALL_NOTICE", "record a bounded notice", "invalid stalled work body")
	}
	return nil
}

func (s *State) applyStalledWorkObserved(op *Op, now time.Time) (Result, []Event, error) {
	worker := s.Agents[op.StalledWorker]
	if worker == nil {
		return nil, nil, errf("E_NO_AGENT", "inspect the worker's current board row",
			"stalled worker %q is gone", op.StalledWorker)
	}
	if prior := worker.StalledWorkNotice; prior != nil && prior.DeclarationSerial >= op.DeclarationSerial {
		return Result{"ok": true, "deduplicated": true}, nil, nil
	}
	worker.StalledWorkNotice = &StalledWorkNotice{
		DeclarationSerial: op.DeclarationSerial, Session: op.SessionID, Host: op.StalledWorkerHost, ObservedAt: now,
	}
	events := []Event{}
	s.finish(&events, now)
	return Result{"ok": true}, events, nil
}

func (s *State) applyStalledWorkNotified(actor *Agent, op *Op, now time.Time) (Result, []Event, error) {
	worker := s.Agents[op.StalledWorker]
	if worker == nil {
		return nil, nil, errf("E_NO_AGENT", "inspect the worker's current board row",
			"stalled worker %q is gone", op.StalledWorker)
	}
	prior := worker.StalledWorkNotice
	if prior != nil && (prior.DeclarationSerial > op.DeclarationSerial ||
		prior.DeclarationSerial == op.DeclarationSerial && prior.NoticeSerial != 0) {
		return Result{"ok": true, "deduplicated": true, "msg_serial": prior.NoticeSerial}, nil, nil
	}
	result, events, err := s.applySend(actor, &Op{
		To: op.To, MsgType: MsgNotify, Body: op.Body, MailboxCapacity: op.MailboxCapacity,
	}, now)
	if err == nil {
		serial, _ := result["msg_serial"].(uint64)
		observed := now
		if prior != nil && prior.DeclarationSerial == op.DeclarationSerial &&
			prior.Session == op.SessionID && prior.Host == op.StalledWorkerHost {
			observed = prior.ObservedAt
		}
		worker.StalledWorkNotice = &StalledWorkNotice{
			DeclarationSerial: op.DeclarationSerial, NoticeSerial: serial,
			Session: op.SessionID, Host: op.StalledWorkerHost, ObservedAt: observed,
		}
	}
	return result, events, err
}
