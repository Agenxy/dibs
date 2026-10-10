// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

func (s *State) admitContactNoticeRead(op *Op) error {
	through := op.ContactNoticeThroughSerial
	if through == 0 {
		return nil
	}
	if op.Kind != OpActivityCheckpoint || op.MsgSerial != 0 || op.OutcomeThroughSerial != 0 {
		return errf("E_BAD_ARG", "contact notice reads belong to their activity checkpoint", "invalid contact read carrier")
	}
	l := s.AgentByToken(op.Token)
	if l == nil {
		return ErrBadToken
	}
	if (op.AgentID != "" && op.AgentID != l.ID) || through < l.CreatedSerial ||
		through < l.ContactNoticeReadAt || through > s.Serial {
		return errf("E_BAD_ARG", "read only your own incarnation's delivered contact prefix", "invalid contact read prefix")
	}
	return nil
}

// Replay folds the recorded read. Historical zero-field checkpoints keep their
// original effect. An explicit duplicate read changes no state or serial.
func (s *State) applyContactCheckpoint(l *Agent, op *Op) (Result, []Event) {
	if op.NotifyAnnounced != nil {
		changed := false
		for _, id := range op.NotifyAnnounced {
			if m := s.Messages[id]; m != nil && m.NotifyAnnouncedAt < max(m.AdoptedAt, 1) {
				m.NotifyAnnouncedAt = s.Serial + 1
				changed = true
			}
		}
		if !changed {
			return Result{"ok": true, "changed": false}, nil
		}
	}
	if op.ContactNoticeThroughSerial != 0 {
		if op.ContactNoticeThroughSerial <= l.ContactNoticeReadAt {
			return Result{"ok": true, "changed": false}, nil
		}
		l.ContactNoticeReadAt = op.ContactNoticeThroughSerial
	}
	return Result{"ok": true}, []Event{}
}
