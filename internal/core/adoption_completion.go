// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

func (s *State) applyRequestedAdoption(
	m *Message, op *Op, from *Agent, res Result, evs []Event, now time.Time,
) []Event {
	res["adopted"], evs[0].Data["adopted"] = from.ID, from.ID
	if op.PriorAdoptionSerial != 0 {
		res["messages"], res["prior_adoption_serial"] = 0, op.PriorAdoptionSerial
		evs[0].Data["prior_adoption_serial"] = op.PriorAdoptionSerial
		res["adopt_note"] = "already performed by the recorded adoption; no messages moved again"
		return evs
	}
	into := s.Agents[m.From]
	// Same ownership/watermark selection as the direct route, including its
	// historical semantics. A later arrival must be moved, never waved through.
	moved := s.readdressMail(from, into, op.V7Semantics)
	res["messages"], res["adopt_note"] = moved, adoptNote(moved)
	evs = append(evs, s.adoptedMailEvents(into, from)...)
	if op.AdoptionRecovery {
		m.PriorAdoptionSerial = 0 // this approval performed a fresh move
		evs = append(evs, s.completeAdoption(from, into, now)...)
	}
	return append(evs, Event{
		Type: "agent.updated", Agent: into.ID,
		Data: map[string]any{"adopted_from": from.ID, "messages": moved, "via": "approved_request"},
	})
}

// CompletedAdoptionOf returns evidence belonging to this request and BOTH
// current incarnations. Empty mail alone never proves that an adoption happened.
// The receipt survives retention of the transferred mail, but not the request.
func (s *State) CompletedAdoptionOf(m *Message) uint64 {
	if m == nil || m.Adopt == "" || m.PriorAdoptionSerial <= m.Serial ||
		m.PriorAdoptionSerial > s.Serial || m.MergedSentAt > m.PriorAdoptionSerial {
		return 0
	}
	from, into := s.Agents[m.Adopt], s.Agents[m.From]
	if from == nil || into.Retired() || from.CreatedSerial > m.Serial || into.CreatedSerial > m.Serial ||
		s.RecoveryCount(from.ID) != 0 {
		return 0
	}
	return m.PriorAdoptionSerial
}

func (s *State) admitPriorAdoption(op *Op) error {
	if op.PriorAdoptionSerial == 0 {
		return nil
	}
	m := s.Messages[op.MsgSerial]
	if !op.AdoptionRecovery || !op.AdoptAuthorised || op.Kind != OpRespond || op.Disposition != "approve" ||
		m == nil || m.Type != MsgRequest || m.Terminal() && m.State != MsgStateQueued ||
		s.CompletedAdoptionOf(m) != op.PriorAdoptionSerial {
		return errf("E_BAD_ADOPTION",
			"approve the pending adoption request using its recorded completed adoption; "+
				"otherwise use adopt_agent for remaining mail",
			"the prior adoption does not satisfy this request")
	}
	return nil
}

// completeAdoption runs inside the transfer's ONE ledger op. Older ops never
// call it. Matching requests get evidence, not an unsolicited approval.
func (s *State) completeAdoption(from, into *Agent, now time.Time) []Event {
	s.noteCompletedAdoptionRequests(from, into)
	evs := s.releaseAdoptedContacts(from, into)
	for _, id := range sortedKeys(s.Contacts) {
		c := s.Contacts[id]
		if c.Recipient != from.ID || !c.ResolvedAt.IsZero() || s.contactStillNeeded(c) {
			continue
		}
		c.ResolvedAt = now
		evs = append(evs, Event{Type: "contact.resolved", Agent: from.ID, Data: map[string]any{"contact_serial": id}})
	}
	return evs
}

func (s *State) noteCompletedAdoptionRequests(from, into *Agent) {
	for _, m := range s.Messages {
		if m.Type == MsgRequest && m.Adopt == from.ID && m.From == into.ID &&
			(!m.Terminal() || m.State == MsgStateQueued) && m.Serial >= from.CreatedSerial && m.Serial >= into.CreatedSerial {
			m.PriorAdoptionSerial = s.Serial + 1
		}
	}
}

func (s *State) releaseAdoptedContacts(from, into *Agent) []Event {
	var evs []Event
	for _, serial := range sortedKeys(s.Messages) {
		m := s.Messages[serial]
		if m.To != into.ID || m.AdoptedFrom != from.ID || m.AdoptedAt != s.Serial+1 {
			continue
		}
		// Asks already emit message.adopted. An escalated high-priority FYI
		// needs that same ordinary arrival path; historical adoption omitted it.
		if m.ContactEscalatedAt != 0 && m.Type == MsgNotify && m.State == MsgStatePending && m.ContactEligible() {
			evs = append(evs, Event{Type: "message.adopted", Agent: from.ID, To: into.ID,
				Data: map[string]any{"msg_serial": m.Serial, "msg_type": m.Type, "from": m.From}})
		}
		// A reachability receipt belongs to the source recipient, not its heir.
		m.ContactEscalatedAt = 0
	}
	return evs
}

func (s *State) contactStillNeeded(c *ContactEscalation) bool {
	if c.MailboxFull {
		return s.MailboxCapacity(c.Recipient).Depth >= s.Limits.MaxMailboxDepth
	}
	for _, m := range s.Messages {
		if m.To == c.Recipient && m.State == MsgStatePending && m.ContactEscalatedAt == c.Serial {
			return true
		}
	}
	return false
}
