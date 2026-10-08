// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

// applyOutcomeRead marks a verdict as read by the agent that asked for it.
//
// Consumed is the RECIPIENT's marker: set when they answer, so it is true for
// every verdict the instant one exists and says nothing about whether the
// asker has seen it. The asker's awareness watermark (AckedSerial) moves only
// on check_in. So the one thing that proved the asker had read an outcome,
// read_mail, wrote nothing replayable, and a daemon restarted between that
// read and the next check_in rebuilt the notice and delivered it again. A
// duplicate, not a loss, and the notification channel is the one thing here
// that has to stay worth reading. Issue #76.
//
// Idempotent and silent: a second read changes nothing and is not ledgered,
// and no event is published, because the reader is the only party this
// concerns and it is the one doing the reading.
func (s *State) applyOutcomeRead(l *Agent, op *Op) (Result, []Event, error) {
	m, ok := s.Messages[op.MsgSerial]
	// Only the new explicit field opts recipients into review reads.
	if ok && m.To == l.ID && m.From != l.ID && op.OutcomeThroughSerial != 0 {
		if op.OutcomeThroughSerial <= max(m.ReviewReadAt, s.ReviewReadCutoff) {
			return Result{"changed": false}, nil, nil
		}
		m.ReviewReadAt = op.OutcomeThroughSerial
		return Result{"changed": true}, []Event{}, nil
	}
	if !ok || m.From != l.ID {
		return nil, nil, errf("E_NO_MESSAGE", "read_mail takes a message you sent or received; "+
			"use inbox() to find your mail", "no message %d sent by you", op.MsgSerial)
	}
	if !m.Terminal() {
		return nil, nil, errf("E_NOT_TERMINAL", "wait for a verdict; there is nothing to have read yet",
			"message %d has no outcome yet", op.MsgSerial)
	}
	through := op.OutcomeThroughSerial
	if through == 0 {
		// Old read ops never carried a prefix. Preserve their one-time verdict
		// meaning even when later reports exist; replay must not invent a read.
		if !m.hasUnreadLegacyOutcome() {
			return Result{"changed": false}, nil, nil
		}
		through = s.Serial + 1 // historical full read
	}
	if through <= m.OutcomeReadAt || !m.HasUnreadOutcome() {
		return Result{"changed": false}, nil, nil
	}
	m.OutcomeReadAt = through
	if m.From == m.To && op.OutcomeThroughSerial != 0 {
		m.ReviewReadAt = max(m.ReviewReadAt, through)
	}
	return Result{"changed": true}, []Event{}, nil
}

func (m *Message) hasUnreadLegacyOutcome() bool {
	return m.OutcomeReadAt == 0 || (m.QueueDebt && m.QueueChangedSerial > m.OutcomeReadAt)
}

// HasUnreadOutcome includes retained reports for new explicit-prefix reads.
// Historical zero-prefix operations use their original one-time rule above.
func (m *Message) HasUnreadOutcome() bool {
	return m.OutcomeReadAt == 0 || m.LatestOutcomeSerial() > m.OutcomeReadAt
}

// LatestOutcomeSerial includes reports made after an approval was read.
// Sender reviews are not new reports to that same sender.
func (m *Message) LatestOutcomeSerial() uint64 {
	latest := m.RespondedAt
	if m.QueueDebt && m.QueueChangedSerial > latest {
		latest = m.QueueChangedSerial
	}
	for _, p := range m.Progress {
		if (p.Review == "" || m.From == m.To) && p.Serial > latest {
			latest = p.Serial
		}
	}
	return latest
}
