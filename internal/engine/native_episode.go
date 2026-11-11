// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"log/slog"
	"slices"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func (e *Engine) restoreNativeTurnHolds() {
	for _, c := range e.state.Contacts {
		l := e.state.Agents[c.Recipient]
		if c.Reason == "" || !c.ResolvedAt.IsZero() || l.Retired() || l.CreatedSerial > c.Serial ||
			!e.ownsHost(l) || surfaceOf(l) != harnessenv.ChatGPTApp {
			continue
		}
		// Pre-fix diagnoses do not retain a binding. A same-incarnation rebind
		// before this fix can restore a stale hold; probe-on-new-mail is the
		// backstop. From here on binding changes resolve the old diagnosis.
		if previous, ok := e.nativeTurnWatches[l.ID]; ok && previous.contact > c.Serial {
			continue
		}
		e.wakers.mu.Lock()
		epoch := e.wakers.queueEpoch
		e.wakers.mu.Unlock()
		if e.nativeTurnWatches == nil {
			e.nativeTurnWatches = map[string]nativeTurnWatch{}
		}
		e.nativeTurnWatches[l.ID] = nativeTurnWatch{
			fence:  nativeContactFence{l.CreatedSerial, wakeSessionOf(l), threadIDOf(l), epoch, e.commandEpoch[l.ID]},
			reason: c.Reason, heldAt: c.Serial, contact: c.Serial, alerted: true,
		}
	}
}

type diagnosedBinding struct {
	created                                uint64
	session, thread, host, hostID, surface string
}

func (e *Engine) diagnosedBinding(c *core.ContactEscalation) diagnosedBinding {
	l := e.state.Agents[c.Recipient]
	if l.Retired() {
		return diagnosedBinding{}
	}
	b := diagnosedBinding{
		created: l.CreatedSerial, session: wakeSessionOf(l), thread: threadIDOf(l), surface: surfaceOf(l),
	}
	if l.Agent != nil {
		b.host, b.hostID = l.Agent.Host, l.Agent.HostID
	}
	return b
}

func (e *Engine) diagnosedBindingsBefore(op *core.Op) map[uint64]diagnosedBinding {
	switch op.Kind {
	case core.OpRegister, core.OpResume, core.OpUpdate, core.OpBindSession, core.OpRelocate,
		core.OpRelocateByHuman, core.OpMergeAgents, core.OpAdoptAgent, core.OpHostRenamed,
		core.OpSignOff, core.OpPrune, core.OpPruneOwn, core.OpSweep:
	default:
		return nil
	}
	before := map[uint64]diagnosedBinding{}
	for id, c := range e.state.Contacts {
		if c.Reason != "" && c.ResolvedAt.IsZero() {
			before[id] = e.diagnosedBinding(c)
		}
	}
	return before
}

func (e *Engine) resolveChangedDiagnosedBindings(before map[uint64]diagnosedBinding, now time.Time) {
	ids := make([]uint64, 0, len(before))
	for id := range before {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := e.state.Contacts[id]
		if c == nil || !c.ResolvedAt.IsZero() || before[id] == e.diagnosedBinding(c) {
			continue
		}
		delete(e.nativeTurnWatches, c.Recipient)
		if _, err := e.applyAndLedger(&core.Op{Kind: core.OpContactResolved, ContactSerial: id}, now); err != nil {
			slog.Error("could not resolve contact after binding change", "contact", id, "err", err)
		}
	}
}

func (e *Engine) nativeEpisode(l *core.Agent) uint64 {
	var serial uint64
	for id, c := range e.state.Contacts {
		if c.Recipient == l.ID && c.Reason != "" && c.ResolvedAt.IsZero() && id >= l.CreatedSerial {
			serial = max(serial, id)
		}
	}
	return serial
}

func (e *Engine) joinNativeEpisode(l *core.Agent, w nativeTurnWatch, now time.Time) {
	c := e.state.Contacts[w.contact]
	if c == nil || !c.ResolvedAt.IsZero() {
		return
	}
	for _, m := range e.state.Inbox(l.ID) {
		if m.State != core.MsgStatePending || m.Consumed || !m.ContactEligible() {
			continue
		}
		op := &core.Op{Kind: core.OpContactJoin, ContactSerial: c.Serial, MsgSerial: m.Serial, AgentID: l.ID}
		if e.state.Admit(op) != nil {
			continue
		}
		if _, err := e.applyAndLedger(op, now); err != nil {
			slog.Error("could not join diagnosed contact", "contact", c.Serial, "msg", m.Serial, "err", err)
			continue
		}
		if e.notifyAppOpenSender(l, m, "", now) {
			e.wakeAppContactSender(m, now)
		}
	}
}
