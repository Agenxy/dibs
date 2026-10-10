// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

// MaxMailboxPage is the maximum number of compact envelopes returned at once.
const MaxMailboxPage = 8

// The selection is a writer-recorded decision. Replay applies it unchanged;
// newer admission bounds must never retroactively reject a historical op.
func (s *State) admitMailboxPage(op *Op) error {
	if op.MailboxSerials == nil {
		if op.Kind == OpAckMailboxPage {
			return errf("E_BAD_ARG", "call check_in to let the writer select the page", "mailbox selection is missing")
		}
		return nil
	}
	bad := func() error {
		return errf("E_BAD_ARG", "call check_in without cursor to restart the mailbox page", "invalid mailbox selection")
	}
	if op.Kind != OpAckMailboxPage || len(*op.MailboxSerials) > MaxMailboxPage {
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
