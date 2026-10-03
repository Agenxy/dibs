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
	if e.wakers.queued == nil {
		e.wakers.queued = map[string]time.Time{}
	}
	e.wakers.queued[agent] = now
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
