// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// A recorded cutoff, not a boot-time wall-clock rule. Replay before this op
// behaves exactly as it did; historical progress/reviews do not flood workers.
// This op first appeared in unreleased #335. Adding its deterministic awareness
// snapshot changes no released ledger's fold, and replay of that unreleased op
// derives the snapshot from the same prior state, without changing old read ops.
func (s *State) applyInitializeReviewRead(op *Op, now time.Time) (Result, []Event, error) {
	if s.ReviewReadCutoff != 0 {
		return Result{"changed": false}, nil, nil
	}
	s.ReviewReadCutoff = op.ReviewReadCutoff
	s.LegacyAckAtCutoff = map[string]LegacyAckSnapshot{}
	for id, agent := range s.Agents {
		if !agent.Retired() {
			s.LegacyAckAtCutoff[id] = LegacyAckSnapshot{agent.CreatedSerial, agent.AckedSerial}
		}
	}
	evs := []Event{}
	s.finish(&evs, now)
	return Result{"changed": true}, evs, nil
}

// LatestReviewSerial is the newest retained review, independently of reports.
func (m *Message) LatestReviewSerial() uint64 {
	var latest uint64
	for _, p := range m.Progress {
		if p.Review != "" && p.Serial > latest {
			latest = p.Serial
		}
	}
	return latest
}

func (s *State) admitOutcomeRead(op *Op) error {
	if op.Kind == OpInitializeReviewRead {
		return s.admitReviewReadCutoff(op)
	}
	if op.ReviewReadCutoff != 0 || (op.OutcomeThroughSerial != 0 && op.Kind != OpOutcomeRead) {
		return errf("E_BAD_ARG", "read fields belong only to their internal read operation", "read field on %s", op.Kind)
	}
	if op.Kind != OpOutcomeRead || op.OutcomeThroughSerial == 0 {
		return nil
	}
	return s.admitOutcomePrefix(op)
}

func (s *State) admitOutcomePrefix(op *Op) error {
	l := s.AgentByToken(op.Token)
	m := s.Messages[op.MsgSerial]
	if l == nil || m == nil {
		return nil // preserve authentication/missing-envelope diagnostics
	}
	if l.CreatedSerial > 0 && m.Serial < l.CreatedSerial && (m.To != l.ID || !s.AdoptedFor(m, l.ID)) {
		return errf("E_NO_MESSAGE", "read only your own retained mail; inbox lists it", "no readable outcome")
	}
	latest := m.LatestOutcomeSerial()
	if m.From != l.ID {
		latest = m.LatestReviewSerial()
	}
	if (m.From != l.ID && m.To != l.ID) || op.OutcomeThroughSerial > latest || op.OutcomeThroughSerial > s.Serial {
		return errf("E_BAD_ARG", "read only the retained outcome prefix delivered to this actor",
			"invalid outcome-read prefix")
	}
	return nil
}

func (s *State) admitReviewReadCutoff(op *Op) error {
	if op.ReviewReadCutoff == 0 || op.ReviewReadCutoff != s.Serial+1 || op.OutcomeThroughSerial != 0 {
		return errf("E_BAD_ARG", "record only the review-read cutoff at the next board serial",
			"invalid review-read cutoff")
	}
	return nil
}
