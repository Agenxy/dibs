// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
)

var watchAntigravityHub = antigravity.WatchDesktop

// Started lazily for the first local binding (or a replayed one at boot).
// Boards without Antigravity neither watch the home directory nor inspect its
// processes. This is one observer per daemon, not one watcher per agent.
func (e *Engine) startAntigravityWatch(ctx context.Context) {
	if e.antigravityWatchStarted {
		return
	}
	bound := false
	for _, l := range e.state.Agents {
		if e.antigravityBinding(l) != 0 {
			bound = true
			break
		}
	}
	if !bound {
		return
	}
	e.antigravityWatchStarted = true
	watch := watchAntigravityHub
	go func() {
		if err := watch(ctx, func(epoch string) { e.observeAntigravityHub(ctx, epoch) }); err != nil && ctx.Err() == nil {
			slog.Error("Antigravity restart observation stopped", "err", err)
			e.ReportFault(ctx, Fault{
				Kind: "antigravity-hub-watch", What: "Antigravity hub changes can no longer be observed",
				Remedy: "Restore access to the Antigravity hub descriptor directory and restart Dibs.",
			})
		}
	}()
}

type antigravityAcceptance struct {
	created, binding    uint64
	session, cwd, epoch string
	items               map[uint64]bool
}

func unreadAntigravityContact(m *core.Message) bool {
	return m != nil && m.State == core.MsgStatePending && !m.Consumed && m.ContactEligible()
}

func (c antigravityAcceptance) matches(e *Engine, l *core.Agent) bool {
	return l != nil && c.created == l.CreatedSerial && c.binding == e.antigravityBinding(l) &&
		c.session == l.CurrentSession && c.cwd == cwdOf(l) && c.epoch == e.commandEpoch[l.ID]
}

// Called on the writer only after the native adapter's actual receipt. It
// records identifiers, not delivery: neither deadlines nor read prefixes move.
func (e *Engine) noteAntigravityAccepted(p wakePlan) {
	if p.antigravity == 0 {
		return
	}
	l := e.state.Agents[p.agent]
	c := antigravityAcceptance{
		created: p.createdSerial, binding: p.antigravity, session: p.session, cwd: p.cwd,
		epoch: p.commandEpoch, items: map[uint64]bool{},
	}
	if !c.matches(e, l) {
		return
	}
	keys := map[string]bool{}
	for _, key := range p.commandKeys {
		keys[key] = true
	}
	for _, m := range e.state.Inbox(p.agent) {
		if unreadAntigravityContact(m) && e.mailBelongsTo(m, l) && keys["mail:"+noticeKey(l.ID, m.Serial)] {
			c.items[m.Serial] = true
		}
	}
	if len(c.items) == 0 {
		return
	}
	if e.antigravityAccepted == nil {
		e.antigravityAccepted = map[string]antigravityAcceptance{}
	}
	e.antigravityAccepted[p.agent] = c
}

// A later actual wake is evidence that the prior accepted notice did not
// establish mail delivery. Ordinary FYIs remain under the existing contact
// eligibility rule; the board never manufactures a read or a failed turn.
func (e *Engine) contactUnreadAntigravityAcceptance(ctx context.Context, plan wakePlan, agent string) {
	res, err := e.query(ctx, func() core.Result {
		c, ok := e.antigravityAccepted[agent]
		return core.Result{"unread_acceptance": ok && e.antigravityContactCurrent(plan, agent, c)}
	})
	if err != nil || res["unread_acceptance"] != true {
		return
	}
	var reason core.ContactReason
	if antigravity.WaitingForCommandApproval(ctx, plan.thread) {
		reason = core.ContactAntigravityApproval
	}
	_, _ = e.query(ctx, func() core.Result {
		e.escalateUnreadAntigravityAcceptance(plan, agent, reason)
		return nil
	})
}

func (e *Engine) antigravityContactCurrent(plan wakePlan, agent string, c antigravityAcceptance) bool {
	l := e.state.Agents[agent]
	return c.matches(e, l) && c.binding == plan.antigravity && l.CurrentSession == plan.thread &&
		cwdOf(l) == plan.cwd && e.originalNativeWakeOwed(plan, agent)
}

func (e *Engine) escalateUnreadAntigravityAcceptance(plan wakePlan, agent string, reason core.ContactReason) {
	c, ok := e.antigravityAccepted[agent]
	if !ok || !e.antigravityContactCurrent(plan, agent, c) {
		return
	}
	l := e.state.Agents[agent]
	ids := make([]uint64, 0, len(c.items))
	for id := range c.items {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		m := e.state.Messages[id]
		if unreadAntigravityContact(m) && e.mailBelongsTo(m, l) && m.ContactEscalatedAt == 0 {
			op := &core.Op{Kind: core.OpContactEscalate, MsgSerial: id}
			if reason != "" {
				op.Kind, op.ContactReason, op.AgentID = core.OpContactDiagnosed, reason, agent
			}
			if e.state.Admit(op) == nil {
				if _, err := e.applyAndLedger(op, time.Now()); err != nil {
					slog.Error("could not record unread Antigravity acceptance contact", "agent", agent, "err", err)
				}
			}
		}
	}
	delete(e.antigravityAccepted, agent)
}

func (e *Engine) observeAntigravityHub(ctx context.Context, epoch string) {
	_, _ = e.query(ctx, func() core.Result {
		if epoch == "" || epoch == e.antigravityHubEpoch {
			return nil
		}
		e.antigravityHubEpoch = epoch
		ids := make([]string, 0)
		for id, l := range e.state.Agents {
			if e.antigravityBinding(l) != 0 && !e.socketOwnsDelivery(l) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			l := e.state.Agents[id]
			if !e.unreadAntigravityMail(l) {
				continue
			}
			e.resetCommandIncarnation(id, epoch) // invalidates old in-flight receipts too
			delete(e.antigravityAccepted, id)
			if !e.noteArrivalDuringWake(id) {
				e.retryWakeDecision(id)
			}
		}
		return nil
	})
}

func (e *Engine) unreadAntigravityMail(l *core.Agent) bool {
	for _, m := range e.state.Inbox(l.ID) {
		if m.UnconsumedEnvelope() && !m.Consumed && m.DeliveredAt == 0 && e.mailBelongsTo(m, l) {
			return true
		}
	}
	return false
}

func (e *Engine) pruneAntigravityAccepted() {
	for id, c := range e.antigravityAccepted {
		l := e.state.Agents[id]
		if !c.matches(e, l) {
			delete(e.antigravityAccepted, id)
			continue
		}
		for serial := range c.items {
			if m := e.state.Messages[serial]; !unreadAntigravityContact(m) || !e.mailBelongsTo(m, l) {
				delete(c.items, serial)
			}
		}
		if len(c.items) == 0 {
			delete(e.antigravityAccepted, id)
		}
	}
}
