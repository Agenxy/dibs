// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"log/slog"
	"time"

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

// openClosedSession recovers a running app session's socket route, or records
// why an ended one waits. Its URL handler shows/focuses a window even with
// open -g, so ended sessions keep mail for an ordinary start or delivering hook.
func (e *Engine) openClosedSession(l *core.Agent) {
	if surfaceOf(l) != harnessenv.ClaudeDesktop || e.remoteHostOf(l) != "" {
		return
	}
	known := knownHarnessSessions(l)
	if len(known) == 0 {
		// There is no CLI session to look up and no background route to recover.
		e.escalateContact(l.ID)
		return
	}
	if l.Status == core.StatusArchived {
		e.noteAppBinding(l, "archived")
	}
	agent, created, thread, session := l.ID, l.CreatedSerial, threadIDOf(l), wakeSessionOf(l)
	finished := e.startWakeWorker(agent, "claude-lineage")
	go func() {
		defer finished()
		if e.deliveryContext().Err() != nil {
			return
		}
		// A resumed app session may be running under a newer CLI id than
		// the writer's peer snapshot. This lookup and process probe do I/O.
		attempt := wakeDiagnostic{
			Route: "claude_desktop", Outcome: "no_background_input_route",
			created: created, session: session, thread: thread, attemptedAt: time.Now(),
		}
		local, current := harnessenv.ClaudeAppSessionNow("", known...)
		if current != "" && claudeRunning(current) && e.deliverToRunningSession(agent, created, thread, known) {
			return
		}
		if sidecar := harnessenv.ClaudeHostSession(known...); sidecar != "" {
			local = sidecar
		}
		_, _ = e.query(e.deliveryContext(), func() core.Result {
			e.recordClosedClaudeRoute(agent, attempt, local)
			return nil
		})
	}()
}

func (e *Engine) recordClosedClaudeRoute(agent string, attempt wakeDiagnostic, appSession string) {
	l := e.state.Agents[agent]
	if l == nil || l.Retired() || l.CreatedSerial != attempt.created || threadIDOf(l) != attempt.thread ||
		wakeSessionOf(l) != attempt.session || surfaceOf(l) != harnessenv.ClaudeDesktop || e.remoteHostOf(l) != "" ||
		e.noCurrentWakeWork(l) || e.hasWakeRoute(l) || e.socketOwnsDelivery(l) ||
		e.lastEvidenceOf(l).After(attempt.attemptedAt) {
		return
	}
	e.noteAppBinding(l, "waiting-thread-open")
	alert := e.appBindings[agent]
	alert.appSession = appSession
	e.appBindings[agent] = alert
	attempt.ObservedAt = time.Now().UTC()
	e.storeWakeDiagnostic(agent, attempt)
	e.escalateContact(agent)
}

var claudeRunning = harnessenv.ClaudeSessionRunning

// Preserve the background half of closed-session recovery: refresh the
// actual peer/lineage cache and offer one socket wake on the writer. No
// app URL or process launch is involved; a missing socket stays unconfirmed.
func (e *Engine) deliverToRunningSession(agent string, created uint64, thread string, known []string) bool {
	if _, ok := peerSessionIn(e.refreshPeersNow(), known); !ok {
		slog.Info("this agent's Claude app session is open but publishes no socket Dibs can reach",
			"agent", agent, "hint", "its mail is delivered at its next Dibs call")
		return false
	}
	res, err := e.query(e.deliveryContext(), func() core.Result {
		l := e.state.Agents[agent]
		if l != nil && !l.Retired() && l.CreatedSerial == created && threadIDOf(l) == thread &&
			surfaceOf(l) == harnessenv.ClaudeDesktop && e.remoteHostOf(l) == "" &&
			(e.hasWakeRoute(l) || e.socketOwnsDelivery(l)) {
			delete(e.appBindings, agent)
			e.retryWakeDecision(agent)
			return core.Result{"reachable": true}
		}
		return nil
	})
	return err == nil && res["reachable"] == true
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
