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
	heldAt  uint64 // only messages written after this hold may probe it
	probe   harnessenv.CodexTurnReceipt
	contact uint64 // replayable diagnosed episode; transient binding stays in fence
	alerted bool   // an ordinary FYI can establish a hold without an alert
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
// The second result identifies the hold episode when this offer is a probe.
func (e *Engine) nativeTurnOffer(p wakePlan) (held bool, probe uint64) {
	var snapshot nativeTurnWatch
	_, err := e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[p.agent]
		w, ok := e.nativeTurnWatches[p.agent]
		if !ok || w.fence != p.nativeContactFence() || !e.acceptedContactMatches(w.fence, l) {
			return nil
		}
		if w.reason != "" {
			e.joinNativeEpisode(l, w, time.Now())
		}
		snapshot = w
		return nil
	})
	if err != nil {
		return false, 0
	}
	if snapshot.reason == "" {
		return snapshot.turns[0].ID != "" && e.observeNativeTurnFailures(p.agent, snapshot), 0
	}
	if e.observeNativeProbe(p.agent, snapshot) {
		return false, 0
	}
	if p.mailArrival <= snapshot.heldAt || !slices.Contains(p.commandKeys, p.nativeProbeKey()) {
		return true, 0
	}
	return false, snapshot.heldAt
}

func (p wakePlan) nativeProbeKey() string {
	return "mail:" + noticeKey(p.agent, p.mailArrival)
}

// Probe receipts are reconciled on the next delivery decision. No timer
// starts a probe or waits for its outcome. A later receipt supersedes an
// older one, even if its input callback finished first.
func (e *Engine) noteNativeProbe(p wakePlan, heldAt uint64, id string, offeredAt time.Time) {
	w, ok := e.nativeTurnWatches[p.agent]
	if !ok || w.heldAt != heldAt || w.fence != p.nativeContactFence() ||
		!e.acceptedContactMatches(w.fence, e.state.Agents[p.agent]) ||
		id == "" || id == w.probe.ID || !offeredAt.After(w.probe.OfferedAt) {
		return
	}
	w.probe = harnessenv.CodexTurnReceipt{ID: id, OfferedAt: offeredAt}
	e.nativeTurnWatches[p.agent] = w
}

func (e *Engine) observeNativeProbe(agent string, w nativeTurnWatch) bool {
	if w.probe.ID == "" {
		return false
	}
	out := harnessenv.ObserveCodexTurns(w.fence.thread, [2]harnessenv.CodexTurnReceipt{w.probe})[0]
	if out.Failure == "" && !out.Normal {
		return false
	}
	r, err := e.query(e.wakeContext, func() core.Result {
		current, ok := e.nativeTurnWatches[agent]
		l := e.state.Agents[agent]
		if !ok || current.heldAt != w.heldAt || current.fence != w.fence || current.probe != w.probe ||
			!e.acceptedContactMatches(w.fence, l) {
			return nil
		}
		if out.Normal {
			delete(e.nativeTurnWatches, agent)
			slog.Debug("native app recovery probe progressed", "agent", agent)
			return core.Result{"recovered": true}
		}
		current.probe.ID = "" // retain offer time as the stale-receipt fence
		if !current.alerted {
			current.alerted = e.escalateNativeTurnMail(l, out.Failure, time.Now())
			current.contact = e.nativeEpisode(l)
		}
		e.nativeTurnWatches[agent] = current
		slog.Debug("native app recovery probe failed early", "agent", agent, "reason", out.Failure)
		return nil
	})
	return err == nil && r["recovered"] == true
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
	turns := harnessenv.ObserveCodexTurns(w.fence.thread, w.turns)
	if turns[0].Failure == "" || turns[1].Failure == "" {
		return false
	}
	reason := turns[1].Failure
	if turns[0].Failure != reason {
		reason = core.ContactTurnsEnded
	}
	r, err := e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[agent]
		current, ok := e.nativeTurnWatches[agent]
		if !ok || current.turns != w.turns || current.fence != w.fence || !e.acceptedContactMatches(w.fence, l) {
			return nil
		}
		if current.reason == "" {
			current.reason, current.heldAt = reason, e.state.Serial
			delete(e.acceptedContacts, agent)
			slog.Warn("native app turns ended before Dibs contact; automatic retries held",
				"agent", agent, "reason", reason, "hint", contactReasonText(reason))
			current.alerted = e.escalateNativeTurnMail(l, reason, time.Now())
			current.contact = e.nativeEpisode(l)
			e.nativeTurnWatches[agent] = current
		}
		return core.Result{"held": true}
	})
	return err == nil && r["held"] == true
}

func (e *Engine) escalateNativeTurnMail(l *core.Agent, reason core.ContactReason, now time.Time) bool {
	alerted := false
	episode := e.nativeEpisode(l)
	for _, m := range e.state.Inbox(l.ID) {
		if m.State != core.MsgStatePending || m.Consumed || !m.ContactEligible() {
			continue
		}
		if m.ContactEscalatedAt != 0 {
			alerted = true
			continue
		}
		// The diagnosis kind makes earlier ledger readers refuse before
		// silently dropping the diagnosis. The actor is for their error path.
		op := &core.Op{Kind: core.OpContactDiagnosed, MsgSerial: m.Serial, ContactReason: reason, AgentID: l.ID}
		if episode != 0 {
			op.Kind, op.ContactSerial = core.OpContactJoin, episode
		}
		if err := e.state.Admit(op); err != nil {
			continue
		}
		if _, err := e.applyAndLedger(op, now); err != nil {
			slog.Error("could not record native turn contact", "agent", l.ID, "msg", m.Serial, "err", err)
		} else {
			alerted = true
			episode = e.nativeEpisode(l)
			if e.notifyAppOpenSender(l, m, "", now) {
				e.wakeAppContactSender(m, now)
			}
		}
	}
	return alerted
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
