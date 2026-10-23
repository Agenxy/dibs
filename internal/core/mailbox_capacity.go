// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// MailboxCapacity is metadata only. Accepted requests remain capacity debt even
// after their approval is terminal; announced FYIs remain readable separately.
type MailboxCapacity struct {
	Depth         int    `json:"depth"`
	Limit         int    `json:"limit"`
	AcceptedDebt  int    `json:"accepted_debt"`
	AnnouncedFYIs int    `json:"announced_fyis"`
	NearCapacity  bool   `json:"near_capacity"`
	Hint          string `json:"hint,omitempty"`
}

// MailboxCapacity counts current admission debt and separately retained FYIs.
func (s *State) MailboxCapacity(agent string) MailboxCapacity {
	var c MailboxCapacity
	for _, m := range s.Messages {
		if m.To == agent {
			s.countMailboxCapacity(&c, m)
		}
	}
	return s.finishMailboxCapacity(c)
}

// Board is a fleet view: aggregate once, rather than rescan all mail per row.
func (s *State) mailboxCapacityWarnings() map[string]MailboxCapacity {
	counts := make(map[string]MailboxCapacity)
	for _, m := range s.Messages {
		c := counts[m.To]
		s.countMailboxCapacity(&c, m)
		counts[m.To] = c
	}
	for id, c := range counts {
		c = s.finishMailboxCapacity(c)
		if c.NearCapacity {
			counts[id] = c
		} else {
			delete(counts, id)
		}
	}
	return counts
}

func (s *State) countMailboxCapacity(c *MailboxCapacity, m *Message) {
	if m.acceptedQueueDebt() {
		c.AcceptedDebt++
		c.Depth++
	}
	if !s.capacityMail(m, m.To) || m.Terminal() {
		return
	}
	if m.FYIPresented() {
		c.AnnouncedFYIs++
	} else {
		c.Depth++
	}
}

func (s *State) finishMailboxCapacity(c MailboxCapacity) MailboxCapacity {
	c.Limit = s.Limits.MaxMailboxDepth
	// ceil(80%): one fifth of the admission budget remains for new handoffs.
	c.NearCapacity = c.Limit > 0 && c.Depth >= c.Limit-c.Limit/5
	if c.NearCapacity {
		c.Hint = "call inbox() for pending asks; respond(done|decline) to finish accepted work; " +
			"ack(notify_through_serial:N) consumes announced FYIs only"
	}
	return c
}

func (s *State) capacityMail(m *Message, agent string) bool {
	return m.To == agent && (m.Serial >= s.mailFloor(agent) || s.adoptedFor(m, agent))
}

// FYIPresented reports a recorded announcement or recipient delivery. An
// adopted FYI cannot inherit the previous recipient's delivery receipt.
func (m *Message) FYIPresented() bool {
	return m.Type == MsgNotify && (m.Consumed || m.NotifyAnnouncedAt >= max(m.AdoptedAt, 1) ||
		(m.State == MsgStateDelivered && m.DeliveredAt >= m.AdoptedAt))
}

func (s *State) capacityDisplacement(op *Op) (*Message, bool) {
	full := s.MailboxCapacity(op.To).Depth >= s.Limits.MaxMailboxDepth
	var oldest *Message
	fyis := 0
	for _, m := range s.Messages {
		if !s.capacityMail(m, op.To) || m.Type != MsgNotify || (m.State != MsgStatePending && m.State != MsgStateDelivered) {
			continue
		}
		fyis++
		if full && m.FYIPresented() {
			continue
		} // giving up an exempt FYI frees no capacity
		if oldest == nil || m.Serial < oldest.Serial {
			oldest = m
		}
	}
	if full {
		return oldest, oldest == nil
	}
	// Exempt FYIs have a separate retention bound. New FYIs may replace their
	// oldest sibling without consuming an ask's admission budget.
	if op.MsgType == MsgNotify && fyis >= s.Limits.MaxMailboxDepth {
		return oldest, false
	}
	return nil, false
}

func (s *State) admitMailboxCapacity(op *Op) error {
	if !op.MailboxCapacity {
		return nil
	}
	if op.Kind != OpSendMessage {
		return errf("E_BAD_ARG", "mailbox capacity is recorded on sends only", "invalid capacity carrier")
	}
	if s.AgentByToken(op.Token) == nil {
		return ErrBadToken
	}
	if op.OpID != "" {
		if l := s.AgentByToken(op.Token); l != nil {
			if _, ok := s.Dedup[dedupKey(l.ID, op.OpID)]; ok {
				return nil
			}
		}
	}
	if !s.Answerable(op.To) {
		return nil
	} // preserve the ordinary unknown-recipient error
	if _, refused := s.capacityDisplacement(op); refused {
		return ErrMailboxFull
	}
	return nil
}

func (s *State) admitBulkFYI(op *Op) error {
	if op.NotifyThroughSerial == 0 {
		return nil
	}
	if op.Kind != OpAckMessage || op.MsgSerial != 0 || op.NotifyThroughSerial > s.Serial ||
		op.NotifyConsumption != "" || op.NotifyAnnounced != nil {
		return errf("E_BAD_ARG", "call ack with either msg_serial or notify_through_serial from check_in, never both",
			"invalid FYI prefix")
	}
	if s.AgentByToken(op.Token) == nil {
		return ErrBadToken
	}
	return nil
}

func (s *State) applyBulkFYI(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	var evs []Event
	count := 0
	// Stable event order is part of the replay contract, including sender notices.
	for _, id := range sortedMessageKeys(s.Messages) {
		m := s.Messages[id]
		if id > op.NotifyThroughSerial || m.Type != MsgNotify || !s.ownMailboxMessage(m, l) ||
			!s.capacityMail(m, l.ID) || !m.FYIPresented() || m.Consumed {
			continue
		}
		m.Consumed = true
		m.NotifyConsumption = "bulk"
		if !m.Terminal() {
			m.State = MsgStateAcked
			m.AckedAt = s.Serial + 1
			m.TerminalAt = now
		}
		count++
		evs = append(evs, Event{
			Type: "message.consumed", Agent: l.ID, To: m.From,
			Data: map[string]any{"msg_serial": m.Serial},
		})
	}
	return Result{"ok": true, "consumed_count": count, "notify_through_serial": op.NotifyThroughSerial}, evs, nil
}
