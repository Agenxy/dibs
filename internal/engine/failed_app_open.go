// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// A queued input is not a usable wake route after its app-open step failed.
// The callback can arrive after a read, rebind or app-incarnation change.
// Keep the original item fence, but do not use freshCommandKeys: acceptance
// already recorded those keys as written, without proving delivery.
func (p wakePlan) strandedQueue() bool {
	if p.queueOutcome == nil || p.queueOutcome.Admission != "retained" || p.queueOutcome.Pending != "pending" {
		return false
	}
	o := p.queueOutcome
	return !o.IssuedAt.IsZero() && o.At.Sub(o.IssuedAt) >= harnessenv.AppOpenRetryAfter
}

func (e *Engine) contactAfterQueuedOpen(plan wakePlan, agent string, failed bool) {
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[agent]
		e.wakers.mu.Lock()
		current := plan.queueEpoch == e.wakers.queueEpoch
		if observation, ok := e.wakers.queueObserved[agent]; ok && plan.queueOutcome != nil {
			current = current && !observation.observation.At.After(plan.queueOutcome.At)
		}
		e.wakers.mu.Unlock()
		if l == nil || l.Retired() || l.CreatedSerial != plan.createdSerial ||
			!current || !l.SessionIsCurrent(plan.session) || e.commandEpoch[agent] != plan.commandEpoch {
			return nil
		}
		for _, m := range e.state.Inbox(agent) {
			if m.State != core.MsgStatePending || !m.ContactEligible() ||
				!slices.Contains(plan.commandKeys, "mail:"+noticeKey(agent, m.Serial)) {
				continue
			}
			e.escalateAppOpenMessage(l, m, failed)
		}
		return nil
	})
}

func (e *Engine) escalateAppOpenMessage(l *core.Agent, m *core.Message, failed bool) {
	op := &core.Op{Kind: core.OpContactEscalate, MsgSerial: m.Serial}
	if err := e.state.Admit(op); err != nil {
		return
	}
	now := time.Now()
	res, err := e.applyAndLedger(op, now)
	if err != nil {
		slog.Error("could not record failed app-open contact", "agent", l.ID, "msg", m.Serial, "err", err)
		return
	}
	if res["deduplicated"] == true {
		return
	}
	reason := "the earlier app wake remains undispatched after the app-open recovery window"
	if failed {
		reason = "the app open failed"
	}
	if e.notifyAppOpenSender(l, m, reason, now) {
		e.wakeAppContactSender(m, now)
	}
}

func (e *Engine) notifyAppOpenSender(l *core.Agent, m *core.Message, reason string, now time.Time) bool {
	c := e.state.Contacts[m.ContactEscalatedAt]
	if c == nil || !c.ResolvedAt.IsZero() || m.From == "" || m.From == l.ID {
		return false
	}
	for _, n := range e.notices[m.From] {
		if n.Kind == "contact.escalated" && n.Serial == c.Serial && n.Msg == 0 {
			return false
		}
	}
	n, _ := e.contactNotice(c)
	hint := n.Contact.OpenHint
	if e.ownsHost(l) && surfaceOf(l) == harnessenv.ChatGPTApp && threadIDOf(l) != "" {
		hint = "open codex://threads/" + threadIDOf(l)
	}
	text := fmt.Sprintf("Message %d remains queued for %s; %s and no started turn or read mail is confirmed. "+
		"Contact alert %d is recorded for the person; %s. OS posting does not prove visibility.",
		m.Serial, l.ID, reason, c.Serial, hint)
	// This is contact news, not an envelope outcome. Reading the still-pending
	// source mail neither presents nor consumes this separate delivery warning.
	e.pushNoticeKind(m.From, text, c.Serial, 0, true, now, "contact.escalated")
	return true
}

func (e *Engine) wakeAppContactSender(m *core.Message, now time.Time) {
	e.coalesceEventWake(core.Event{
		Type: "contact.escalated", Agent: "dibs", To: m.From, Serial: m.ContactEscalatedAt, TS: now,
		Data: map[string]any{"contact_serial": m.ContactEscalatedAt, "from": "dibs", "msg_type": "notice"},
	})
}

// Contact state is durable even after its event left the replay ring. Rebuild
// sender warnings without guessing the transient failure cause. The existing
// authenticated contact-read prefix prevents repeated warnings after restart.
func (e *Engine) rebuildAppContactSenders() {
	if e.state == nil {
		return
	}
	serials := make([]uint64, 0, len(e.state.Messages))
	for serial := range e.state.Messages {
		serials = append(serials, serial)
	}
	slices.Sort(serials)
	for _, serial := range serials {
		m := e.state.Messages[serial]
		c, sender := e.state.Contacts[m.ContactEscalatedAt], e.state.Agents[m.From]
		if c == nil || c.MailboxFull || !c.ResolvedAt.IsZero() || sender.Retired() ||
			c.Serial <= sender.ContactNoticeReadAt || m.State != core.MsgStatePending || !m.ContactEligible() {
			continue
		}
		recipient := e.state.Agents[c.Recipient]
		if recipient == nil || (surfaceOf(recipient) != harnessenv.ChatGPTApp &&
			surfaceOf(recipient) != harnessenv.ClaudeDesktop) {
			continue
		}
		e.notifyAppOpenSender(recipient, m, "delivery to the original app is still unconfirmed", c.WindowStart)
	}
}
