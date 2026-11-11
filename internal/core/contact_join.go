// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

func (s *State) admitContactJoin(op *Op) error {
	c, m := s.Contacts[op.ContactSerial], s.Messages[op.MsgSerial]
	if c == nil || c.Reason == "" || !c.ResolvedAt.IsZero() || m == nil || m.To != c.Recipient ||
		m.State != MsgStatePending || m.Consumed || !m.ContactEligible() ||
		(m.ContactEscalatedAt != 0 && m.ContactEscalatedAt != c.Serial) {
		return errf("E_BAD_CONTACT", "join unread eligible mail to its recipient's unresolved diagnosed contact",
			"invalid contact episode")
	}
	return nil
}

// Explicit episode choice is ledgered. Historical contact escalation operations
// retain their time-window rule, and older readers refuse this unknown kind.
func (s *State) applyContactJoin(op *Op, now time.Time) (Result, []Event, error) {
	c, m := s.Contacts[op.ContactSerial], s.Messages[op.MsgSerial]
	if m.ContactEscalatedAt == c.Serial {
		return Result{"ok": true, "deduplicated": true, "contact_serial": c.Serial}, nil, nil
	}
	c.Count++
	if c.OldestSerial == 0 || m.Serial < c.OldestSerial {
		c.OldestSerial = m.Serial
	}
	c.HighWater = max(c.HighWater, m.Serial)
	m.ContactEscalatedAt = c.Serial
	var evs []Event
	s.finish(&evs, now)
	return Result{"ok": true, "coalesced": true, "contact_serial": c.Serial}, evs, nil
}
