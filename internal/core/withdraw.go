package core

import "time"

const (
	// OpWithdrawMessage retracts a sender's unfinished request or question.
	OpWithdrawMessage = "withdraw_message"
	// MsgStateWithdrawn is a retracted message, never an answer or completed work.
	MsgStateWithdrawn = "withdrawn"
)

func checkWithdrawal(op *Op, lim Limits) error {
	withdraw := op.Kind == OpWithdrawMessage || (op.Kind == OpRespond && op.Disposition == "withdraw")
	if !withdraw {
		if op.SupersededBy != 0 {
			return errf("E_BAD_ARG", "superseded_by belongs to respond(withdraw)", "replacement on another operation")
		}
		return nil
	}
	if op.MsgSerial == 0 || op.SupersededBy == op.MsgSerial {
		return errf("E_BAD_ARG", "name a request or question serial and a different replacement, if any",
			"invalid withdrawal serial")
	}
	if len(op.Body) > lim.MaxBodyBytes {
		return errTooLarge("withdrawal reason", lim.MaxBodyBytes)
	}
	if op.Milestone != 0 || len(op.Milestones) != 0 || op.Deliverable != "" {
		return errf("E_BAD_ARG", "withdraw takes body as reason and optional superseded_by, not work reports",
			"recipient fields on withdrawal")
	}
	return nil
}

// SenderOwnsRequest uses the same creation fence as read_mail, in both
// directions. Adoption grants recipient access, never sender authority.
func SenderOwnsRequest(m *Message, l *Agent) bool {
	return senderOwnsMessage(m, l) && m.Type == MsgRequest
}

func senderOwnsMessage(m *Message, l *Agent) bool {
	return m != nil && l != nil && m.From == l.ID &&
		(l.CreatedSerial == 0 || m.Serial >= l.CreatedSerial)
}

func senderOwnsWithdrawable(m *Message, l *Agent) bool {
	return senderOwnsMessage(m, l) && (m.Type == MsgRequest || m.Type == MsgQuestion)
}

// Only this NEW op reaches these state checks. Historical responses keep
// exactly their old interpretation on replay.
func (s *State) applyWithdraw(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	m := s.Messages[op.MsgSerial]
	if !senderOwnsWithdrawable(m, l) {
		return nil, nil, errf("E_NOT_SENDER", "withdraw a request or question YOU sent; read_mail shows its sender",
			"not your request or question")
	}
	if err := withdrawalEligibility(m); err != nil {
		return nil, nil, err
	}
	if op.SupersededBy != 0 {
		replacement := s.Messages[op.SupersededBy]
		if !senderOwnsWithdrawable(replacement, l) || replacement.Type != m.Type ||
			replacement.Grant != "" || replacement.Adopt != "" {
			return nil, nil, errf("E_NO_MESSAGE", "superseded_by names another ordinary message of the same type YOU sent",
				"replacement is not your message of the same type")
		}
	}
	queued := m.State == MsgStateQueued
	m.State, m.WithdrawalReason, m.WithdrawnBy = MsgStateWithdrawn, op.Body, l.ID
	m.SupersededBy = op.SupersededBy
	m.QueueDebt, m.QueueOrderLocked, m.QueueRank, m.QueueLockBy = false, false, 0, ""
	m.Consumed = false // sender cannot acknowledge the recipient's receipt
	m.AckedAt = 0
	m.TerminalAt, m.RespondedAt = now, s.Serial+1
	m.OutcomeReadAt = s.Serial + 1 // sender has this call's receipt
	if op.RetainUntil != nil {
		m.RetainUntil = op.RetainUntil.UTC()
	}
	data := map[string]any{"msg_serial": m.Serial, "message_type": m.Type}
	if m.SupersededBy != 0 {
		data["superseded_by"] = m.SupersededBy
	}
	evs := []Event{{Type: "message.withdrawn", Agent: l.ID, To: m.To, Data: data}}
	if queued {
		s.setQueueOrder(s.TaskQueue(m.To), l.ID, now)
		evs = append(evs, s.queueEvents(s.TaskQueue(m.To), l.ID, "message.queue_changed", 0)...)
	}
	s.finish(&evs, now)
	return Result{
		"ok": true, "state": MsgStateWithdrawn, "msg_serial": m.Serial, "superseded_by": m.SupersededBy,
	}, evs, nil
}

func withdrawalEligibility(m *Message) error {
	switch m.State {
	case MsgStatePending, MsgStateDelivered, MsgStateAcked:
		return nil
	case MsgStateQueued:
		if m.Type == MsgRequest {
			return nil
		}
	case MsgStateApproved:
		if m.Type == MsgRequest && m.Grant == "" && m.Adopt == "" {
			return nil
		}
		return errf("E_MSG_FINAL", "approval already performed this effect; withdrawal cannot undo it",
			"request already performed")
	}
	return errf("E_MSG_FINAL", "this message is finished; send a new request or question instead",
		"message already %s", m.State)
}
