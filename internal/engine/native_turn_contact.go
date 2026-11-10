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

// Only two distinct accepted turns per local agent are retained. An actual
// authenticated model call clears the evidence, as does a new wake binding.
type nativeTurnWatch struct {
	fence   nativeContactFence
	turns   [2]harnessenv.CodexTurnReceipt
	checkAt time.Time
	checked bool
	reason  core.ContactReason
}

func (e *Engine) noteNativeTurn(p wakePlan, id string, offeredAt, now time.Time) {
	l := e.state.Agents[p.agent]
	fence := p.nativeContactFence()
	if id == "" || offeredAt.IsZero() || !e.acceptedContactMatches(fence, l) {
		return
	}
	if contact := e.contact[p.agent]; contact.created == l.CreatedSerial && !contact.at.Before(offeredAt) {
		return
	}
	w, exists := e.nativeTurnWatches[p.agent]
	if !exists || !e.acceptedContactMatches(w.fence, l) ||
		now.Sub(w.turns[1].OfferedAt) > acceptedContactAfter {
		w = nativeTurnWatch{fence: fence}
	}
	if id == w.turns[0].ID || id == w.turns[1].ID {
		return // steering can return the same running turn
	}
	w.turns[0], w.turns[1] = w.turns[1], harnessenv.CodexTurnReceipt{ID: id, OfferedAt: offeredAt}
	w.checkAt, w.checked = now.Add(2*time.Second), false
	if e.nativeTurnWatches == nil {
		e.nativeTurnWatches = map[string]nativeTurnWatch{}
	}
	e.nativeTurnWatches[p.agent] = w
}

// This is before a further native submission. It also covers a new message
// that arrives just ahead of the sweep, without waiting for that sweep.
func (e *Engine) nativeTurnDeliveryHeld(p wakePlan) bool {
	var snapshot nativeTurnWatch
	r, err := e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[p.agent]
		w, ok := e.nativeTurnWatches[p.agent]
		if !ok || w.fence != p.nativeContactFence() || !e.acceptedContactMatches(w.fence, l) {
			return nil
		}
		if w.reason != "" {
			e.escalateNativeTurnMail(l, w.reason, time.Now())
			return core.Result{"held": true}
		}
		snapshot = w
		return nil
	})
	if err != nil {
		return false
	}
	if r["held"] == true {
		return true
	}
	return snapshot.turns[0].ID != "" && e.observeNativeTurnFailures(p.agent, snapshot)
}

// The existing sweep takes at most one quiet-thread observation per pair.
// Normal turns check in and discard the pair before any filesystem read.
func (e *Engine) reconsiderNativeTurns(now time.Time) {
	var ready []string
	for agent, w := range e.nativeTurnWatches {
		if !e.acceptedContactMatches(w.fence, e.state.Agents[agent]) ||
			(w.reason == "" && now.Sub(w.turns[1].OfferedAt) > acceptedContactAfter) {
			delete(e.nativeTurnWatches, agent)
			continue
		}
		if w.reason == "" && w.turns[0].ID != "" && !w.checked && !now.Before(w.checkAt) {
			ready = append(ready, agent)
		}
	}
	slices.Sort(ready)
	for _, agent := range ready {
		w := e.nativeTurnWatches[agent]
		w.checked = true
		e.nativeTurnWatches[agent] = w
		go e.observeNativeTurnFailures(agent, w)
	}
}

func (e *Engine) observeNativeTurnFailures(agent string, w nativeTurnWatch) bool {
	if e.wakeContext.Err() != nil {
		return false
	}
	failures := harnessenv.CodexEarlyTurnFailures(w.fence.thread, w.turns)
	if failures[0] == "" || failures[1] == "" {
		return false
	}
	reason := failures[1]
	if failures[0] != reason {
		reason = core.ContactTurnsEnded
	}
	r, err := e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[agent]
		current, ok := e.nativeTurnWatches[agent]
		if !ok || current.turns != w.turns || current.fence != w.fence || !e.acceptedContactMatches(w.fence, l) {
			return nil
		}
		if current.reason == "" {
			current.reason = reason
			e.nativeTurnWatches[agent] = current
			delete(e.acceptedContacts, agent)
			slog.Warn("native app turns ended before Dibs contact; further native notices held",
				"agent", agent, "reason", reason, "hint", contactReasonText(reason))
		}
		e.escalateNativeTurnMail(l, current.reason, time.Now())
		return core.Result{"held": true}
	})
	return err == nil && r["held"] == true
}

func (e *Engine) escalateNativeTurnMail(l *core.Agent, reason core.ContactReason, now time.Time) {
	for _, m := range e.state.Inbox(l.ID) {
		if m.State != core.MsgStatePending || m.Consumed || !m.ContactEligible() || m.ContactEscalatedAt != 0 {
			continue
		}
		// The versioned kind makes earlier ledger readers refuse before
		// silently dropping the diagnosis. The actor is for their error path.
		op := &core.Op{Kind: core.OpContactDiagnosed, MsgSerial: m.Serial, ContactReason: reason, AgentID: l.ID}
		if err := e.state.Admit(op); err != nil {
			continue
		}
		if _, err := e.applyAndLedger(op, now); err != nil {
			slog.Error("could not record native turn contact", "agent", l.ID, "msg", m.Serial, "err", err)
		}
	}
}

func contactReasonText(reason core.ContactReason) string {
	const remedy = " Compact the original thread and have it check Dibs, or move the agent to a new thread."
	switch reason {
	case core.ContactContextFull:
		return "The thread's context is full and ChatGPT cannot compact it. Its turns end before reading mail." + remedy
	case core.ContactCompactionFailed:
		return "ChatGPT reports a compaction failure and its turns end before reading mail." + remedy
	case core.ContactTurnsEnded:
		return "ChatGPT turns end immediately without reading mail. " +
			"Open the original thread to inspect the failure or move the agent."
	}
	return ""
}
