// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

// MaxMailboxPage is the maximum number of compact envelopes returned at once.
const MaxMailboxPage = 8

// The selection is a writer-recorded decision. Replay applies it unchanged;
// newer admission bounds must never retroactively reject a historical op.
func (s *State) admitMailboxPage(op *Op) error {
	if err := s.admitNotifyReceipt(op); err != nil {
		return err
	}
	if op.MailboxSerials == nil {
		return nil
	}
	bad := func() error {
		return errf("E_BAD_ARG", "call check_in without cursor to restart the mailbox page", "invalid mailbox selection")
	}
	if op.Kind != OpActivityCheckpoint || op.ContactNoticeThroughSerial != 0 || len(*op.MailboxSerials) > MaxMailboxPage {
		return bad()
	}
	l := s.AgentByToken(op.Token)
	if l == nil {
		return bad()
	}
	seen := map[uint64]bool{}
	for _, id := range *op.MailboxSerials {
		m := s.Messages[id]
		if m == nil || m.To != l.ID || seen[id] || (m.Serial < l.CreatedSerial && !s.AdoptedFor(m, l.ID)) {
			return bad()
		}
		seen[id] = true
	}
	return nil
}

func (s *State) admitNotifyReceipt(op *Op) error {
	if op.NotifyConsumption == "" && op.NotifyAnnounced == nil {
		return nil
	}
	l := s.AgentByToken(op.Token)
	if l == nil {
		return ErrBadToken
	}
	bad := func() error {
		return errf("E_BAD_ARG", "FYI receipts are recorded by bounded recipient presentation", "invalid FYI receipt")
	}
	ids, err := notifyReceiptSelection(op)
	if err != nil {
		return bad()
	}
	seen := map[uint64]bool{}
	for _, id := range ids {
		m := s.Messages[id]
		if m == nil || m.Type != MsgNotify || !s.ownMailboxMessage(m, l) || seen[id] {
			return bad()
		}
		seen[id] = true
	}
	return nil
}

func (s *State) ownMailboxMessage(m *Message, l *Agent) bool {
	return m.To == l.ID && (m.Serial >= l.CreatedSerial || s.AdoptedFor(m, l.ID))
}

func notifyReceiptSelection(op *Op) ([]uint64, error) {
	bad := func() ([]uint64, error) {
		return nil, errf("E_BAD_ARG", "FYI receipts are writer-recorded", "invalid FYI receipt carrier")
	}
	if op.NotifyConsumption != "" {
		validReason := op.NotifyConsumption == "presented" || op.NotifyConsumption == "reminded"
		if op.Kind != OpAckMessage || !validReason || op.NotifyAnnounced != nil {
			return bad()
		}
		return []uint64{op.MsgSerial}, nil
	}
	if op.Kind != OpActivityCheckpoint || op.MailboxSerials != nil || op.ContactNoticeThroughSerial != 0 {
		return bad()
	}
	if len(op.NotifyAnnounced) == 0 || len(op.NotifyAnnounced) > MaxMailboxPage {
		return bad()
	}
	return op.NotifyAnnounced, nil
}
