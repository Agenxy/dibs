// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// One queued wake at a time.
//
// The command route QUEUES: `codex queue` stores the message durably in the
// ChatGPT app, and the app releases one queued message each time a turn of
// that thread ends. Every wake Dibs ran while the thread was not loaded was
// therefore kept, and they drained one per turn later: measured 2026-10-01,
// codex-k7-0 was handed "Dibs: a new request is waiting." at 15:35:05,
// 15:35:23 and 15:35:37, then a run of "a new notice/notify is waiting.",
// each starting a turn with nothing in its inbox, each turn's end releasing
// the next. Reported by k7-dev as wakes with no answerable request behind
// them, which is exactly what they were.
//
// So while a queued wake has not been picked up (no prompt/start hook since it
// was queued), another is not added: when that one delivers, the agent checks
// in and sees all of its mail, which is what every later wake would have
// asked it to do. queuedWakeTTL bounds the wait, so a queued message that was
// lost (the app reinstalled, the thread deleted) does not silence the agent
// for good.

const queuedWakeTTL = 2 * time.Hour

// ONLY A COMMAND THAT QUEUES. An operator's own command may deliver each time
// it runs (a pager, a notifier), and holding its second run would lose a
// message rather than dedupe one. `codex queue` is the command known to keep
// what it is given, and a Codex agent on another machine is reached by that
// machine's bridge, which allows `codex queue` and nothing else for Codex.
func isQueuingArgv(argv []string) bool {
	if len(argv) < 2 || filepath.Base(argv[0]) != "codex" {
		return false
	}
	for _, a := range argv[1:] {
		if !strings.HasPrefix(a, "-") {
			return a == "queue"
		}
	}
	return false
}

// queues reports whether a plan that ran was a queuing wake.
func queues(plan wakePlan) bool {
	if plan.host != "" {
		return plan.request.Harness == "codex"
	}
	return isQueuingArgv(plan.argv)
}

// queuesFor reports whether this agent's route queues, before a plan exists.
// Caller holds wakers.mu.
func queuesFor(l *core.Agent, byHarness map[string]wakeCommand) bool {
	h := wakeHarness(l)
	if cmd, ok := byHarness[h]; ok {
		return isQueuingArgv(cmd.argv)
	}
	return h == "codex" // reached through another machine's bridge
}

// noteQueuedWake records a command wake that succeeded.
func (e *Engine) noteQueuedWake(agent string, now time.Time) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	e.wakers.noteQueuedWakeLocked(agent, now)
}

func (w *wakers) noteQueuedWakeLocked(agent string, now time.Time) {
	if w.queued == nil {
		w.queued = map[string]time.Time{}
	}
	w.queued[agent] = now
}

type queuedMailOffer struct {
	at      time.Time
	fence   nativeContactFence
	serials []uint64
}

// Record the offer and its cohort together on the writer. Timestamp at
// attempt start so a prompt hook during the command can still precede Stop.
func (e *Engine) noteQueuedMailOffer(plan wakePlan, started time.Time, unread []uint64) {
	w := &e.wakers
	w.mu.Lock()
	defer w.mu.Unlock()
	if started.Before(w.queued[plan.agent]) || plan.queueEpoch != w.queueEpoch {
		return
	}
	w.noteQueuedWakeLocked(plan.agent, started)
	if len(unread) == 0 {
		delete(w.queuedMail, plan.agent)
		return
	}
	if w.queuedMail == nil {
		w.queuedMail = map[string]queuedMailOffer{}
	}
	w.queuedMail[plan.agent] = queuedMailOffer{started, plan.nativeContactFence(), unread}
}

// A later prompt and real Stop establish that a turn has passed. Relinquish
// only the last cohort still awaiting its recipient's read. The next ordinary
// actionable event can offer it, subject to the same queue admission gate.
// Stop itself submits nothing. An ambiguous submission is never in this cohort.
func (e *Engine) releaseUnreadQueuedOffer(l *core.Agent) {
	w := &e.wakers
	w.mu.Lock()
	offer, ok := w.queuedMail[l.ID]
	f := offer.fence
	if !ok || f.created != l.CreatedSerial || !l.SessionIsCurrent(f.session) || f.thread != threadIDOf(l) ||
		f.commandEpoch != e.commandEpoch[l.ID] || f.queueEpoch != w.queueEpoch || offer.at != w.queued[l.ID] ||
		!w.queuedPrompt[l.ID].After(offer.at) {
		w.mu.Unlock()
		return
	}
	delete(w.queuedMail, l.ID)
	w.mu.Unlock()
	for _, serial := range offer.serials {
		m := e.state.Messages[serial]
		if unreadContactAsk(m) && e.mailBelongsTo(m, l) {
			delete(e.commandWritten, "mail:"+noticeKey(l.ID, serial))
		}
	}
}

// wakeStillQueuedLocked reports whether the last command wake for this agent is
// still waiting to be delivered. Caller holds wakers.mu, on the writer loop
// (queuedPrompt is set only by the current thread's prompt/start hook).
func (e *Engine) wakeStillQueuedLocked(l *core.Agent, now time.Time) bool {
	at, ok := e.wakers.queued[l.ID]
	if !ok || now.Sub(at) >= queuedWakeTTL {
		return false
	}
	return !e.wakers.queuedPrompt[l.ID].After(at)
}

// holdForQueuedWakeLocked is the whole decision for wakeFor: a command route
// that queues, with a wake still undelivered. Caller holds wakers.mu.
func (e *Engine) holdForQueuedWakeLocked(l *core.Agent, byCommand bool, now time.Time) bool {
	if cmd, ok := e.wakers.byHarness[wakeHarness(l)]; byCommand && ok && wakeexec.UsesQueueReceipt(cmd.argv) {
		// The shared runner asks the real queue outside the writer loop. A
		// cached inference here must not hide its authoritative empty result.
		return false
	}
	if !byCommand || !queuesFor(l, e.wakers.byHarness) || !e.wakeStillQueuedLocked(l, now) {
		return false
	}
	slog.Debug("no wake: the last one is still queued in the agent's app, and will "+
		"deliver all its mail when it does", "agent", l.ID)
	return true
}
