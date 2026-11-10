// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Measured accepted-notice/model-call associations are normally seconds; the
// longest responsive pair in the initial one-seat sample was about 16 minutes.
// This is a contact threshold, not a claim that the recipient's process died.
const acceptedContactAfter = 30 * time.Minute

// Writer-owned observations, never coordination state. Restart loses the
// receipt, not mail: a later actual acceptance establishes a fresh interval.
type nativeContactFence struct {
	created         uint64
	session, thread string
	queueEpoch      uint64
	commandEpoch    string
}

func (p wakePlan) nativeContactFence() nativeContactFence {
	return nativeContactFence{
		created: p.createdSerial, session: p.session, thread: p.thread,
		queueEpoch: p.queueEpoch, commandEpoch: p.commandEpoch,
	}
}

type acceptedContact struct {
	fence nativeContactFence
	items map[uint64]time.Time
}

func unreadContactAsk(m *core.Message) bool {
	return m != nil && m.State == core.MsgStatePending && !m.Consumed &&
		m.Type != core.MsgNotify && m.ContactEligible()
}

func (e *Engine) acceptedContactMatches(c nativeContactFence, l *core.Agent) bool {
	if l == nil || l.Retired() || l.CreatedSerial != c.created ||
		!e.ownsHost(l) || surfaceOf(l) != harnessenv.ChatGPTApp ||
		!l.SessionIsCurrent(c.session) || threadIDOf(l) != c.thread ||
		e.commandEpoch[l.ID] != c.commandEpoch {
		return false
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	return c.queueEpoch == e.wakers.queueEpoch
}

// Enter only after the real native adapter accepted input, on the writer.
// A model call may arrive between input acceptance and this callback: compare
// with attempt start, rather than mistaking callback completion for delivery.
func (e *Engine) noteAcceptedContact(p wakePlan, started, now time.Time) {
	l := e.state.Agents[p.agent]
	fence := p.nativeContactFence()
	if !e.acceptedContactMatches(fence, l) {
		return
	}
	if contact := e.contact[l.ID]; contact.created == l.CreatedSerial && !contact.at.Before(started) {
		return
	}
	c, ok := e.acceptedContacts[l.ID]
	if !ok || !e.acceptedContactMatches(c.fence, l) {
		c = acceptedContact{fence, map[uint64]time.Time{}}
	}
	e.captureAcceptedMail(c.items, p.commandKeys, l, now)
	if len(c.items) == 0 {
		return
	}
	if e.acceptedContacts == nil {
		e.acceptedContacts = map[string]acceptedContact{}
	}
	e.acceptedContacts[l.ID] = c
	e.considerAcceptedContact(l.ID, now)
}

func (e *Engine) captureAcceptedMail(items map[uint64]time.Time, keys []string, l *core.Agent, now time.Time) {
	for _, key := range keys {
		kind, item, _ := strings.Cut(key, ":")
		who, raw, _ := strings.Cut(item, "\x00")
		id, err := strconv.ParseUint(raw, 10, 64)
		m := e.state.Messages[id]
		if kind != "mail" || err != nil || who != l.ID || !unreadContactAsk(m) || !e.mailBelongsTo(m, l) {
			continue
		}
		if _, exists := items[id]; !exists {
			items[id] = now // the FIRST acceptance, never the most recent one
		}
	}
}

func (e *Engine) considerAcceptedContact(agent string, now time.Time) {
	c, ok := e.acceptedContacts[agent]
	if !ok {
		return
	}
	l := e.state.Agents[agent]
	if !e.acceptedContactMatches(c.fence, l) {
		delete(e.acceptedContacts, agent)
		return
	}
	ids := make([]uint64, 0, len(c.items))
	for id := range c.items {
		ids = append(ids, id)
	}
	slices.Sort(ids) // stable contact oldest/high-water order, independent of maps
	for _, id := range ids {
		m := e.state.Messages[id]
		if !unreadContactAsk(m) || !e.mailBelongsTo(m, l) || m.ContactEscalatedAt != 0 {
			delete(c.items, id)
			continue
		}
		if now.Sub(c.items[id]) < acceptedContactAfter {
			continue
		}
		op := &core.Op{Kind: core.OpContactEscalate, MsgSerial: id}
		if err := e.state.Admit(op); err != nil {
			continue
		}
		if _, err := e.applyAndLedger(op, now); err != nil {
			slog.Error("could not record delivery-unconfirmed contact", "agent", agent, "msg", id, "err", err)
			continue
		}
		delete(c.items, id)
	}
	if len(c.items) == 0 {
		delete(e.acceptedContacts, agent)
	}
}

func (e *Engine) reconsiderAcceptedContacts(now time.Time) {
	agents := make([]string, 0, len(e.acceptedContacts))
	for agent := range e.acceptedContacts {
		agents = append(agents, agent)
	}
	slices.Sort(agents)
	for _, agent := range agents {
		e.considerAcceptedContact(agent, now)
	}
}

// UndeliveredForSeconds is a projection, never a field in the message or its
// ledger. Recipient awareness removes it without changing response deadlines.
func UndeliveredForSeconds(m *core.Message, now time.Time) *int64 {
	if !unreadContactAsk(m) || m.DeliveredAt != 0 || m.SentAt.IsZero() {
		return nil
	}
	age := max(int64(0), int64(now.Sub(m.SentAt)/time.Second))
	return &age
}
