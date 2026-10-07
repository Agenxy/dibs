// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package mailhistory projects committed canonical mail transitions. It is a
// rebuildable in-memory view; the encrypted ledger remains the only store.
package mailhistory

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Metadata deliberately excludes all participant content (including paths,
// labels and expiry explanations). Comparable snapshots detect silent reads
// and GC without copying bodies or reproducing transition rules.
type Metadata struct {
	From             string    `json:"from"`
	To               string    `json:"to"`
	Type             string    `json:"type"`
	State            string    `json:"state"`
	Consumed         bool      `json:"consumed"`
	AdoptedFrom      string    `json:"adopted_from,omitempty"`
	AdoptedAt        uint64    `json:"adopted_serial,omitempty"`
	Delivered        uint64    `json:"delivered_serial,omitempty"`
	Responded        uint64    `json:"responded_serial,omitempty"`
	Acked            uint64    `json:"acked_serial,omitempty"`
	OutcomeRead      uint64    `json:"outcome_read_serial,omitempty"`
	ReviewRead       uint64    `json:"review_read_serial,omitempty"`
	ReviewCutoff     uint64    `json:"review_cutoff_serial,omitempty"`
	Superseded       uint64    `json:"superseded_by,omitempty"`
	SentAt           time.Time `json:"sent_at,omitzero"`
	DeliveredAt      time.Time `json:"delivered_at,omitzero"`
	TerminalAt       time.Time `json:"terminal_at,omitzero"`
	RetainUntil      time.Time `json:"retain_until,omitzero"`
	Deadline         time.Time `json:"deadline,omitzero"`
	QueuePriority    string    `json:"queue_priority,omitempty"`
	RequestPriority  string    `json:"request_priority,omitempty"`
	QueueRank        int       `json:"queue_rank,omitempty"`
	QueueDebt        bool      `json:"queue_debt,omitempty"`
	QueueOrderLocked bool      `json:"queue_order_locked,omitempty"`
	QueueChanged     uint64    `json:"queue_changed_serial,omitempty"`
	Milestones       int       `json:"milestone_count,omitempty"`
	Progress         int       `json:"progress_count,omitempty"`
}

func metadata(m *core.Message) Metadata {
	return Metadata{
		From: m.From, To: m.To, Type: m.Type, State: m.State, Consumed: m.Consumed,
		AdoptedFrom: m.AdoptedFrom, AdoptedAt: m.AdoptedAt, Delivered: m.DeliveredAt,
		Responded: m.RespondedAt, Acked: m.AckedAt,
		OutcomeRead: m.OutcomeReadAt, ReviewRead: m.ReviewReadAt, Superseded: m.SupersededBy,
		SentAt: m.SentAt, DeliveredAt: m.DeliveredTime, TerminalAt: m.TerminalAt,
		RetainUntil: m.RetainUntil, Deadline: m.Deadline,
		QueuePriority: m.QueuePriority, QueueRank: m.QueueRank, QueueDebt: m.QueueDebt,
		RequestPriority:  m.RequestPriority,
		QueueOrderLocked: m.QueueOrderLocked, QueueChanged: m.QueueChangedSerial,
		Milestones: len(m.Milestones), Progress: len(m.Progress),
	}
}

// Author records the identity incarnation and machine at an authored unit.
type Author struct {
	ID      string `json:"agent"`
	Created uint64 `json:"created_serial,omitempty"`
	Host    string `json:"host,omitempty"`
}

// Snapshot is transient and contains no text. Capture before Apply; Observe
// only after successful persistence (or a validated replay record). The maps
// are writer-owned scratch: no observer may retain them past the next Capture.
type Snapshot struct {
	Mail       map[uint64]Metadata
	Authors    map[string]Author
	all        bool
	newMessage uint64
	prepared   bool
}

// Capture copies only canonical metadata before the state machine mutates it.
func Capture(st *core.State, op *core.Op, scratch *Snapshot) Snapshot {
	scope, _ := scopeOf(op.Kind)
	if op.Kind == core.OpRespond && op.Disposition == "approve" {
		if m := st.Messages[op.MsgSerial]; m != nil && m.Adopt != "" {
			scope = scopeFull
		}
	}
	scratch.prepare(scope)
	actor := st.Agents[op.AgentID]
	if actor == nil && op.Token != "" {
		actor = st.AgentByToken(op.Token)
	}
	switch scope {
	case scopeFull:
		for _, m := range st.Messages {
			scratch.captureMessage(st, m)
		}
	case scopeMailbox:
		scratch.captureMailbox(st, op, actor)
	case scopeListed:
		for _, serial := range op.MsgSerials {
			scratch.captureMessage(st, st.Messages[serial])
		}
	case scopePoint:
		scratch.captureMessage(st, st.Messages[op.MsgSerial])
	case scopeNone:
	}
	scratch.captureAuthor(actor)
	return *scratch
}

func (s Snapshot) captureAuthor(a *core.Agent) {
	if a != nil {
		s.Authors[a.ID] = authorOf(a)
	}
}

func stateMetadata(st *core.State, m *core.Message) Metadata {
	meta := metadata(m)
	meta.ReviewCutoff = st.ReviewReadCutoff
	return meta
}

func authorOf(a *core.Agent) Author {
	host := ""
	if a.Agent != nil {
		host = a.Agent.HostID
	}
	return Author{ID: a.ID, Created: a.CreatedSerial, Host: host}
}
