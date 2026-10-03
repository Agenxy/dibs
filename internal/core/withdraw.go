package core

import "time"

const (
	OpWithdrawMessage = "withdraw_message"
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
		return errf("E_BAD_ARG", "name a request serial and a different replacement, if any", "invalid withdrawal serial")
	}
	if len(op.Body) > lim.MaxBodyBytes {
		return errTooLarge("withdrawal reason", lim.MaxBodyBytes)
	}
	if op.Milestone != 0 || len(op.Milestones) != 0 || op.Deliverable != "" {
		return errf("E_BAD_ARG", "withdraw takes body as reason and optional superseded_by, not work reports", "recipient fields on withdrawal")
	}
	return nil
}

// SenderOwnsRequest uses the same creation fence as read_mail, in both
// directions. Adoption grants recipient access, never sender authority.
func SenderOwnsRequest(m *Message, l *Agent) bool {
	return m != nil && l != nil && m.Type == MsgRequest && m.From == l.ID &&
		(l.CreatedSerial == 0 || m.Serial >= l.CreatedSerial)
}

// Only this NEW op reaches these state checks. Historical responses keep
// exactly their old interpretation on replay.
func (s *State) applyWithdraw(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	m := s.Messages[op.MsgSerial]
	if !SenderOwnsRequest(m, l) {
		return nil, nil, errf("E_NOT_SENDER", "withdraw a request YOU sent; read_mail shows its sender", "not your request")
	}
	switch m.State {
	case MsgStatePending, MsgStateDelivered, MsgStateQueued:
	case MsgStateApproved:
		if m.Grant != "" || m.Adopt != "" {
			return nil, nil, errf("E_MSG_FINAL", "approval already performed this effect; withdrawal cannot undo it", "request already performed")
		}
	default:
		return nil, nil, errf("E_MSG_FINAL", "this request is finished; send a new request instead", "request already %s", m.State)
	}
	if op.SupersededBy != 0 {
		replacement := s.Messages[op.SupersededBy]
		if !SenderOwnsRequest(replacement, l) || replacement.Grant != "" || replacement.Adopt != "" {
			return nil, nil, errf("E_NO_MESSAGE", "superseded_by names another ordinary request YOU sent", "replacement is not your work request")
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
	data := map[string]any{"msg_serial": m.Serial}
	if m.SupersededBy != 0 {
		data["superseded_by"] = m.SupersededBy
	}
	evs := []Event{{Type: "message.withdrawn", Agent: l.ID, To: m.To, Data: data}}
	if queued {
		s.setQueueOrder(s.TaskQueue(m.To), l.ID, now)
		evs = append(evs, s.queueEvents(s.TaskQueue(m.To), l.ID, "message.queue_changed", 0)...)
	}
	s.finish(&evs, now)
	return Result{"ok": true, "state": MsgStateWithdrawn, "msg_serial": m.Serial, "superseded_by": m.SupersededBy}, evs, nil
}
