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
	"github.com/agenxy/dibs/internal/wakeexec"
)

const queuedContactUnknownAfter = 30 * time.Minute

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

// At most one unresolved recovery window or app query per agent. A due
// window is checked by the existing sweep without touching the ledger.
type queuedOpenContact struct {
	plan         wakePlan
	due          time.Time
	unknownSince time.Time
	failed       bool
	checking     bool
}

func sameQueuedOpen(a, b wakePlan) bool {
	return a.createdSerial == b.createdSerial && a.session == b.session && a.thread == b.thread &&
		a.queueEpoch == b.queueEpoch && a.commandEpoch == b.commandEpoch &&
		a.queueOutcome != nil && b.queueOutcome != nil && a.queueOutcome.IssuedAt.Equal(b.queueOutcome.IssuedAt)
}

// A subsequent observation of the same retained input joins the pending
// window. Starting a fresh open every time its memo expires could otherwise
// renew the window before it ever reports a stranded thread.
func (e *Engine) queuedOpenRecoveryPending(plan wakePlan, agent string) bool {
	r, err := e.query(e.wakeContext, func() core.Result {
		pending, ok := e.queuedOpenContacts[agent]
		if !ok || !sameQueuedOpen(pending.plan, plan) || !e.queuedOpenCurrent(plan, e.state.Agents[agent]) {
			return nil
		}
		pending.plan = joinQueuedOpenPlan(pending.plan, plan)
		e.queuedOpenContacts[agent] = pending
		return core.Result{"pending": true}
	})
	return err == nil && r["pending"] == true
}

func joinQueuedOpenPlan(previous, current wakePlan) wakePlan {
	keys := slices.Concat(previous.commandKeys, current.commandKeys)
	slices.Sort(keys)
	current.commandKeys = slices.Compact(keys)
	return current
}

func (e *Engine) contactAfterQueuedOpen(plan wakePlan, agent string, failed bool) {
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[agent]
		if !e.queuedOpenCurrent(plan, l) {
			return nil
		}
		now := time.Now()
		if failed && !plan.canObserveQueuedContact() {
			delete(e.queuedOpenContacts, agent)
			e.escalateQueuedOpen(plan, l, true, "", now)
			return nil
		}
		if e.queuedOpenContacts == nil {
			e.queuedOpenContacts = map[string]queuedOpenContact{}
		}
		if pending, ok := e.queuedOpenContacts[agent]; ok && sameQueuedOpen(pending.plan, plan) {
			pending.plan = joinQueuedOpenPlan(pending.plan, plan)
			pending.failed = pending.failed || failed
			e.queuedOpenContacts[agent] = pending
			return nil
		}
		pending := queuedOpenContact{plan: plan, due: now.Add(harnessenv.AppOpenRetryAfter), failed: failed}
		if failed {
			pending.due = now
		}
		e.queuedOpenContacts[agent] = pending
		if failed {
			e.checkQueuedOpenContact(agent, pending, now)
		}
		return nil
	})
}

func (e *Engine) queuedOpenCurrent(plan wakePlan, l *core.Agent) bool {
	if l == nil || l.Retired() || l.Status == core.StatusArchived || l.CreatedSerial != plan.createdSerial ||
		!l.SessionIsCurrent(plan.session) || threadIDOf(l) != plan.thread || !e.ownsHost(l) ||
		e.commandEpoch[l.ID] != plan.commandEpoch {
		return false
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if plan.queueEpoch != e.wakers.queueEpoch {
		return false
	}
	observation, ok := e.wakers.queueObserved[l.ID]
	return !ok || plan.queueOutcome == nil || !observation.observation.At.After(plan.queueOutcome.At)
}

func (e *Engine) reconsiderQueuedOpenContacts(now time.Time) {
	var ready []string
	for agent, pending := range e.queuedOpenContacts {
		if !pending.checking && !now.Before(pending.due) {
			ready = append(ready, agent)
		}
	}
	slices.Sort(ready)
	for _, agent := range ready {
		pending := e.queuedOpenContacts[agent]
		e.checkQueuedOpenContact(agent, pending, now)
	}
}

func (p wakePlan) canObserveQueuedContact() bool {
	return p.host == "" && p.surface == harnessenv.ChatGPTApp && p.thread != "" &&
		p.queueOutcome != nil && p.queueOutcome.ProbeBinary != "" && !p.queueOutcome.IssuedAt.IsZero()
}

// The writer checks the original notice cohort and binding before and after
// the bounded app-server call. One in-flight check serves that cohort.
func (e *Engine) checkQueuedOpenContact(agent string, pending queuedOpenContact, now time.Time) {
	l := e.state.Agents[agent]
	if !e.queuedOpenCurrent(pending.plan, l) {
		delete(e.queuedOpenContacts, agent)
		return
	}
	if !pending.plan.canObserveQueuedContact() {
		delete(e.queuedOpenContacts, agent)
		e.escalateQueuedOpen(pending.plan, l, pending.failed, "", now)
		return
	}
	if !e.queuedContactHasMail(pending.plan, agent) {
		delete(e.queuedOpenContacts, agent)
		return
	}
	pending.checking = true
	e.queuedOpenContacts[agent] = pending
	finish := e.startWakeWorker(agent, "queued-contact")
	go func() {
		defer finish()
		queue := pending.plan.queueOutcome
		evidence := wakeexec.ObserveQueueContact(
			e.wakeContext, queue.ProbeBinary, pending.plan.thread, queue.IssuedAt, now.Add(-contactActivityGrace))
		_, _ = e.query(e.wakeContext, func() core.Result {
			e.finishQueuedOpenContact(agent, pending.plan, evidence, now)
			return nil
		})
	}()
}

func (e *Engine) finishQueuedOpenContact(
	agent string, plan wakePlan, evidence wakeexec.QueueContactEvidence, now time.Time,
) {
	pending, ok := e.queuedOpenContacts[agent]
	if !ok || !pending.checking || !sameQueuedOpen(pending.plan, plan) {
		return
	}
	l := e.state.Agents[agent]
	if !e.queuedOpenCurrent(pending.plan, l) || !e.queuedContactHasMail(pending.plan, agent) {
		delete(e.queuedOpenContacts, agent)
		return
	}
	var reason core.ContactReason
	switch evidence {
	case wakeexec.QueueContactGone:
		delete(e.queuedOpenContacts, agent)
		return
	case wakeexec.QueueContactReady:
		// The quiet thread and matching item authorize the usual alert below.
	case wakeexec.QueueContactActive:
		pending.unknownSince = time.Time{}
	case wakeexec.QueueContactWait:
		if pending.unknownSince.IsZero() {
			pending.unknownSince = now
		}
		if now.Sub(plan.queueOutcome.IssuedAt) >= queuedContactUnknownAfter &&
			now.Sub(pending.unknownSince) >= contactActivityGrace {
			reason = core.ContactQueueStateUnknown
		}
	}
	if evidence == wakeexec.QueueContactReady || reason != "" {
		delete(e.queuedOpenContacts, agent)
		e.escalateQueuedOpen(pending.plan, l, pending.failed, reason, now)
		return
	}
	pending.checking = false
	pending.due = now.Add(contactActivityGrace)
	e.queuedOpenContacts[agent] = pending
}

func (e *Engine) queuedContactHasMail(plan wakePlan, agent string) bool {
	for _, m := range e.state.Inbox(agent) {
		if queuedContactMail(plan, agent, m) {
			return true
		}
	}
	return false
}

func queuedContactMail(plan wakePlan, agent string, m *core.Message) bool {
	return m.State == core.MsgStatePending && !m.Consumed && m.ContactEligible() &&
		slices.Contains(plan.commandKeys, "mail:"+noticeKey(agent, m.Serial))
}

func (e *Engine) escalateQueuedOpen(
	plan wakePlan, l *core.Agent, failed bool, reason core.ContactReason, now time.Time,
) {
	for _, m := range e.state.Inbox(l.ID) {
		if queuedContactMail(plan, l.ID, m) {
			e.escalateAppOpenMessage(l, m, failed, reason, now)
		}
	}
}

func (e *Engine) escalateAppOpenMessage(
	l *core.Agent, m *core.Message, failed bool, diagnosis core.ContactReason, now time.Time,
) {
	op := &core.Op{Kind: core.OpContactEscalate, MsgSerial: m.Serial}
	if diagnosis != "" {
		op.Kind = core.OpContactDiagnosed
		op.AgentID = l.ID
		op.ContactReason = diagnosis
	}
	if err := e.state.Admit(op); err != nil {
		return
	}
	res, err := e.applyAndLedger(op, now)
	if err != nil {
		slog.Error("could not record failed app-open contact", "agent", l.ID, "msg", m.Serial, "err", err)
		return
	}
	if res["deduplicated"] == true {
		return
	}
	reason := "mail remains unread after the app-open recovery window"
	if failed {
		reason = "the queued input's app open failed"
	}
	if e.notifyAppOpenSender(l, m, reason, now) {
		e.wakeAppContactSender(m, now)
	}
}

func (e *Engine) notifyAppOpenSender(l *core.Agent, m *core.Message, reason string, now time.Time) bool {
	c := e.state.Contacts[m.ContactEscalatedAt]
	if !e.senderContactWarningDue(l, m, c) {
		return false
	}
	for _, n := range e.notices[m.From] {
		if n.Kind == "contact.escalated" && n.Serial == c.Serial && n.Msg == 0 {
			return false
		}
	}
	n, _ := e.contactNotice(c)
	hint := n.Contact.OpenHint
	if e.ownsHost(l) && surfaceOf(l) == harnessenv.ChatGPTApp && threadIDOf(l) != "" && !e.bindingNeedsRecovery(l) {
		hint = "open codex://threads/" + threadIDOf(l)
	}
	text := fmt.Sprintf("Message %d to %s: delivery unconfirmed; %s and no started turn or read mail is confirmed. "+
		"Contact alert %d is recorded for the person; %s. OS posting does not prove visibility.",
		m.Serial, l.ID, reason, c.Serial, hint)
	if c.Reason != "" {
		text = fmt.Sprintf("Delivery to %s remains unconfirmed. %s "+
			"Contact alert %d is recorded for the person; %s. OS posting does not prove visibility.",
			l.ID, contactReasonText(c.Reason), c.Serial, hint)
	}
	if alert, ok := e.closedClaudeBinding(l); ok {
		text = fmt.Sprintf("Message #%d to %s. %s", m.Serial, l.ID, e.closedClaudeSendNote(l, m, alert))
	} else if !c.NotifiedAt.IsZero() && surfaceOf(l) == harnessenv.ClaudeDesktop {
		text = fmt.Sprintf("Contact alert #%d for message #%d to %s was posted for the person. %s.",
			c.Serial, m.Serial, l.ID, hint)
	}
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
		if c.Reason == core.ContactClaudeUnavailable && c.NotifiedAt.IsZero() {
			continue // the next posting attempt will produce the sender's outcome
		}
		switch surfaceOf(recipient) {
		case harnessenv.ChatGPTApp, harnessenv.ClaudeDesktop, harnessenv.ChatGPTCloudExec:
			e.notifyAppOpenSender(recipient, m, "delivery to the original app is still unconfirmed", c.WindowStart)
		}
	}
}

func (e *Engine) senderContactWarningDue(l *core.Agent, m *core.Message, c *core.ContactEscalation) bool {
	sender := e.state.Agents[m.From]
	return c != nil && c.ResolvedAt.IsZero() && m.From != "" && m.From != l.ID && !sender.Retired() &&
		c.Serial > sender.ContactNoticeReadAt && e.contactSenderPending(m.From, c.Serial) &&
		!e.senderContactWithdrawn(m.From, c.Serial)
}
