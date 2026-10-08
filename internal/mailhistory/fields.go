// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

import "time"

const fieldCount = 35

const fullFields uint64 = 1<<fieldCount - 1

type fieldKind uint8

const (
	wideField fieldKind = iota
	narrowField
	textField
	timeField
	signedField
)

// These positions belong only to the disposable derived representation.
var fieldKinds = [fieldCount]fieldKind{
	wideField, wideField, narrowField, timeField, textField,
	textField, textField, textField, textField, textField, narrowField,
	wideField, wideField, wideField, wideField, wideField, wideField,
	wideField, wideField, wideField, wideField,
	timeField, timeField, timeField, timeField, timeField,
	textField, signedField, signedField, signedField, textField, textField,
	textField, wideField, wideField,
}

type fieldValue struct {
	number uint64
	text   string
	at     time.Time
}

func canonicalFields(u *snapshotUnit) [fieldCount]fieldValue {
	m := &u.Metadata
	return [fieldCount]fieldValue{
		{number: u.Position.Op},
		{number: u.Position.Msg},
		{number: uint64(u.Position.Ord)},
		{at: u.At},
		{text: u.Kind},
		{text: m.From},
		{text: m.To},
		{text: m.Type},
		{text: m.State},
		{text: m.AdoptedFrom},
		{number: snapshotFlags(*u)},
		{number: m.AdoptedAt},
		{number: m.Delivered},
		{number: m.Responded},
		{number: m.Acked},
		{number: m.OutcomeRead},
		{number: m.ReviewRead},
		{number: m.ReviewCutoff},
		{number: m.Superseded},
		{number: m.QueueChanged},
		{number: u.Author.Created},
		{at: m.SentAt},
		{at: m.DeliveredAt},
		{at: m.TerminalAt},
		{at: m.RetainUntil},
		{at: m.Deadline},
		{text: m.QueuePriority},
		{number: uint64(m.QueueRank)},  // #nosec G115 -- native signed bit pattern, restored to int
		{number: uint64(m.Milestones)}, // #nosec G115 -- same-width signed bit pattern
		{number: uint64(m.Progress)},   // #nosec G115 -- same-width signed bit pattern
		{text: u.Author.ID},
		{text: u.Author.Host},
		{text: m.RequestPriority},
		{number: u.FromCreated},
		{number: u.ToCreated},
	}
}

func restoreFields(f [fieldCount]fieldValue) snapshotUnit {
	flags := f[10].number
	return snapshotUnit{
		Position: position{f[0].number, f[1].number, uint32(f[2].number)}, // #nosec G115 -- checked narrow field
		At:       f[3].at, Kind: f[4].text,
		Metadata: Metadata{
			From: f[5].text, To: f[6].text, Type: f[7].text, State: f[8].text, AdoptedFrom: f[9].text,
			Consumed: flags&1 != 0, QueueDebt: flags&2 != 0, QueueOrderLocked: flags&4 != 0,
			AdoptedAt: f[11].number, Delivered: f[12].number, Responded: f[13].number, Acked: f[14].number,
			OutcomeRead: f[15].number, ReviewRead: f[16].number, ReviewCutoff: f[17].number,
			Superseded: f[18].number, QueueChanged: f[19].number,
			SentAt: f[21].at, DeliveredAt: f[22].at, TerminalAt: f[23].at, RetainUntil: f[24].at, Deadline: f[25].at,
			QueuePriority:   f[26].text,
			QueueRank:       int(f[27].number), // #nosec G115 -- restores native signed bit pattern
			Milestones:      int(f[28].number), // #nosec G115 -- restores native signed bit pattern
			Progress:        int(f[29].number), // #nosec G115 -- restores native signed bit pattern
			RequestPriority: f[32].text,
		},
		Author:  Author{ID: f[30].text, Created: f[20].number, Host: f[31].text},
		Content: flags&8 != 0, AfterKnown: flags&16 != 0, Evicted: flags&32 != 0,
		FromCreated: f[33].number, ToCreated: f[34].number,
	}
}

// Primitive comparisons use stack values only. This runs on the canonical
// replay/writer path; encoding, timestamp serialization and compression do not.
func changedFields(u, previous *snapshotUnit) uint64 {
	if previous == nil {
		return fullFields
	}
	m, p := &u.Metadata, &previous.Metadata
	var mask uint64
	for bit, changed := range [fieldCount]bool{
		u.Position.Op != previous.Position.Op, u.Position.Msg != previous.Position.Msg,
		u.Position.Ord != previous.Position.Ord, u.At != previous.At, u.Kind != previous.Kind,
		m.From != p.From, m.To != p.To, m.Type != p.Type, m.State != p.State, m.AdoptedFrom != p.AdoptedFrom,
		snapshotFlags(*u) != snapshotFlags(*previous),
		m.AdoptedAt != p.AdoptedAt, m.Delivered != p.Delivered, m.Responded != p.Responded,
		m.Acked != p.Acked, m.OutcomeRead != p.OutcomeRead, m.ReviewRead != p.ReviewRead,
		m.ReviewCutoff != p.ReviewCutoff, m.Superseded != p.Superseded, m.QueueChanged != p.QueueChanged,
		u.Author.Created != previous.Author.Created,
		m.SentAt != p.SentAt, m.DeliveredAt != p.DeliveredAt, m.TerminalAt != p.TerminalAt,
		m.RetainUntil != p.RetainUntil, m.Deadline != p.Deadline,
		m.QueuePriority != p.QueuePriority, m.QueueRank != p.QueueRank, m.Milestones != p.Milestones,
		m.Progress != p.Progress, u.Author.ID != previous.Author.ID, u.Author.Host != previous.Author.Host,
		m.RequestPriority != p.RequestPriority, u.FromCreated != previous.FromCreated, u.ToCreated != previous.ToCreated,
	} {
		if changed {
			mask |= 1 << bit
		}
	}
	return mask
}
