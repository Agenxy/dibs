// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"log/slog"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Waking an agent IN the app it runs in.
//
// An agent belongs to the environment it started in and is woken there and
// nowhere else. For a ChatGPT-app agent, the operator's `codex queue` puts the
// message in the thread's queue, and the app delivers it when it has the thread
// loaded: at once if it already does, and only when somebody opens the thread
// if it does not, which after an app update was every thread. So when the app
// is not holding the thread, Dibs asks the app to open it through the app's own
// codex:// route, which loads it in the app's runtime and launches the app
// first if it is closed. The agent then acts in the app the operator is
// looking at.
//
// Never anywhere else. Opening a thread in an app the agent did not run in is a
// relocation, not a wake, so this happens only for an agent whose bridge
// DERIVED the app from its process tree, or, when no bridge has said, whose
// thread was born in the app (see harnessenv.AppFor). A Codex agent in a
// terminal is never opened in the app.

// shower is the contact with the real app, replaced by tests. Keep a pointer:
// a copied value at package init precedes TestMain's fake and can retain the
// real opener in a test binary.
var shower = &harnessenv.RealShower

// showInApp runs after the message was queued, off the writer loop like every
// wake. A failure here is logged and does not fail the wake: the message is in
// the thread's queue, and the app delivers it whenever the thread is opened.
//
// Which app is decided HERE, off the writer loop, because when no bridge has
// stated one it is read from the thread's transcript on disk (harnessenv.AppFor).
func (e *Engine) showInApp(plan wakePlan, agent string) {
	if plan.thread == "" {
		return
	}
	if !e.appNavigationAllowed(agent, plan.createdSerial, plan.thread) {
		return
	}
	if stale, err := harnessenv.ChatGPTBindingStale(plan.thread); err != nil || stale {
		if stale && e.wakeContext != nil {
			_, _ = e.query(e.wakeContext, func() core.Result { e.noteAppBinding(e.state.Agents[agent], "stale"); return nil })
		}
		return
	}
	argv := harnessenv.OpenArgv(harnessenv.AppFor(plan.surface, plan.harness, plan.thread), plan.thread)
	stranded := plan.strandedQueue()
	if stranded {
		if e.queuedOpenRecoveryPending(plan, agent) {
			return
		}
		slog.Info("earlier app wake is still undispatched; retrying the original thread's app-open path",
			"agent", agent, "hint", "queue retention is not delivery; an alert waits for the app-open recovery window")
	}
	s := e.appShower(agent, plan.createdSerial, plan.thread)
	s.ShowWhenIdle(argv, plan.thread, func(opened, deferred bool, err error) {
		logShow(opened, deferred, err, "agent", agent)
		if deferred || errors.Is(err, harnessenv.ErrAppOpenPairBusy) {
			return // lock contention did not attempt an open
		}
		if err != nil || stranded {
			e.contactAfterQueuedOpen(plan, agent, err != nil)
		}
	})
}

// logShow says what happened to one open, for whoever reads the log later.
func logShow(opened, deferred bool, err error, args ...any) {
	switch {
	case err != nil:
		slog.Warn("could not open the agent's thread in its app; the message waits there "+
			"until the thread is opened", append(args, "err", err)...)
	case deferred:
		slog.Info("the agent's thread is not loaded in its app; waiting briefly for the background "+
			"wake lock; the notice stays queued until the thread runs", args...)
	case opened:
		slog.Info("opened the agent's thread in the app it runs in; queued input remains unconfirmed", args...)
	}
}

func surfaceOf(l *core.Agent) string {
	if l == nil || l.Agent == nil {
		return ""
	}
	return l.Agent.Surface
}

// openClosedSession records why this app's ended session cannot be woken.
// Its URL handler explicitly shows/focuses a window even with open -g. Mail
// remains stored for the session's next ordinary start or delivering hook.
func (e *Engine) openClosedSession(l *core.Agent) {
	if surfaceOf(l) != harnessenv.ClaudeDesktop {
		return
	}
	if l.Gone() {
		e.noteAppBinding(l, "archived")
		return
	}
	e.noteAppBinding(l, "waiting-thread-open")
	slog.Debug("waiting for its thread to be opened", "agent", l.ID,
		"reason", "the app's ended-session route navigates and takes focus; mail remains stored")
}

// knownHarnessSessions is every thread-shaped session id Dibs has recorded for
// an agent, oldest first: the bound one, its aliases, then the current one.
// Lineage matching wants all of them, unlike the socket route, which must
// reach only the current activation (sessionsOf).
func knownHarnessSessions(l *core.Agent) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range append(append([]string{l.SessionID}, l.SessionAliases...), l.CurrentSession) {
		if looksLikeThreadID(s) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
