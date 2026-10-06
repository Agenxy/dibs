package core

import "time"

// ContactWindow is the minimum spacing between human contact escalations for
// one recipient. The fold keeps every eligible source within the window in one
// bounded summary, without copying a participant's body into a system notice.
const ContactWindow = 10 * time.Minute

// ContactEscalation is replayable proof that Dibs could not reach an agent and
// asked for help reaching it. NotifiedAt means an OS posting receipt arrived;
// merely queuing or attempting a notification is not that proof.
type ContactEscalation struct {
	Serial       uint64    `json:"serial"`
	Recipient    string    `json:"recipient"`
	OldestSerial uint64    `json:"oldest_serial"`
	HighWater    uint64    `json:"high_water"`
	Count        int       `json:"count"`
	WindowStart  time.Time `json:"window_start"`
	NotifiedAt   time.Time `json:"notified_at,omitzero"`
	ResolvedAt   time.Time `json:"resolved_at,omitzero"`
}

func (s *State) latestContact(recipient string) *ContactEscalation {
	var latest *ContactEscalation
	for _, c := range s.Contacts {
		if c.Recipient == recipient && (latest == nil || c.Serial > latest.Serial) {
			latest = c
		}
	}
	return latest
}

func (s *State) applyContactEscalate(op *Op, now time.Time) (Result, []Event, error) {
	m := s.Messages[op.MsgSerial]
	if m == nil {
		return nil, nil, errf("E_NO_MESSAGE", "inspect pending mail before escalating", "message %d is gone", op.MsgSerial)
	}
	if m.ContactEscalatedAt != 0 {
		return Result{"ok": true, "deduplicated": true, "contact_serial": m.ContactEscalatedAt}, nil, nil
	}
	c := s.latestContact(m.To)
	// An unposted contact remains one outstanding plea for help. A quiet
	// desktop or absent relay must not produce a new alert every ten minutes.
	if c == nil || !c.ResolvedAt.IsZero() || (!c.NotifiedAt.IsZero() &&
		(now.Before(c.WindowStart) || !now.Before(c.WindowStart.Add(ContactWindow)))) {
		c = &ContactEscalation{
			Serial: s.Serial + 1, Recipient: m.To, OldestSerial: m.Serial,
			HighWater: m.Serial, Count: 1, WindowStart: now,
		}
		if s.Contacts == nil {
			s.Contacts = map[uint64]*ContactEscalation{}
		}
		s.Contacts[c.Serial] = c
		coordinator := ""
		for _, id := range sortedKeys(s.Agents) {
			l := s.Agents[id]
			if id != m.To && !l.Retired() && l.IsCoordinator() {
				coordinator = id
				break
			}
		}
		ev := []Event{{Type: "contact.escalated", Agent: m.To, To: coordinator, Data: map[string]any{
			"contact_serial": c.Serial, "msg_serial": m.Serial,
		}}}
		s.finish(&ev, now)
		m.ContactEscalatedAt = c.Serial
		return Result{"ok": true, "contact_serial": c.Serial, "coalesced": false}, ev, nil
	}
	c.Count++
	if m.Serial < c.OldestSerial {
		c.OldestSerial = m.Serial
	}
	if m.Serial > c.HighWater {
		c.HighWater = m.Serial
	}
	m.ContactEscalatedAt = c.Serial
	// A coalesced count is state. It needs a ledger serial even though it should
	// not send a second human notification in this window.
	var ev []Event
	s.finish(&ev, now)
	return Result{"ok": true, "contact_serial": c.Serial, "coalesced": true}, ev, nil
}

func (s *State) applyContactNotified(op *Op, now time.Time) (Result, []Event, error) {
	c := s.Contacts[op.ContactSerial]
	if c == nil {
		return nil, nil, errf("E_NO_CONTACT", "read the outstanding contact notices", "contact %d is gone", op.ContactSerial)
	}
	if !c.NotifiedAt.IsZero() {
		return Result{"ok": true, "deduplicated": true}, nil, nil
	}
	c.NotifiedAt = now
	ev := []Event{{
		Type: "contact.notified", Agent: c.Recipient,
		Data: map[string]any{"contact_serial": c.Serial},
	}}
	s.finish(&ev, now)
	return Result{"ok": true, "contact_serial": c.Serial}, ev, nil
}

func (s *State) applyContactResolved(op *Op, now time.Time) (Result, []Event, error) {
	c := s.Contacts[op.ContactSerial]
	if c == nil {
		return nil, nil, errf("E_NO_CONTACT", "read the outstanding contact notices", "contact %d is gone", op.ContactSerial)
	}
	if !c.ResolvedAt.IsZero() {
		return Result{"ok": true, "deduplicated": true}, nil, nil
	}
	c.ResolvedAt = now
	ev := []Event{{
		Type: "contact.resolved", Agent: c.Recipient,
		Data: map[string]any{"contact_serial": c.Serial},
	}}
	s.finish(&ev, now)
	return Result{"ok": true, "contact_serial": c.Serial}, ev, nil
}
