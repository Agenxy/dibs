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
	if !ok || m.From != l.ID {
		return nil, nil, errf("E_NO_MESSAGE", "read_mail takes a message you sent or received; "+
			"use inbox() to find your mail", "no message %d sent by you", op.MsgSerial)
	}
	if !m.Terminal() {
		return nil, nil, errf("E_NOT_TERMINAL", "wait for a verdict; there is nothing to have read yet",
			"message %d has no outcome yet", op.MsgSerial)
	}
	if !m.HasUnreadOutcome() {
		return Result{"changed": false}, nil, nil
	}
	m.OutcomeReadAt = s.Serial + 1
	return Result{"changed": true}, []Event{}, nil
}

// HasUnreadOutcome preserves the historical one-time read marker for old mail.
// New queued debt also tracks ordering changes recorded after the last read.
func (m *Message) HasUnreadOutcome() bool {
	return m.OutcomeReadAt == 0 || (m.QueueDebt && m.QueueChangedSerial > m.OutcomeReadAt)
}
