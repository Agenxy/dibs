// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func nativeDeliveryAccepted(disposition string) bool {
	return disposition == "accepted" || disposition == "started" || disposition == "steered"
}

func (p wakePlan) commandDelivery() bool {
	return p.host != "" || len(p.argv) > 0 || p.antigravity != 0
}

func (p wakePlan) retryAllowed() bool {
	return !p.commandOutcome.NoRetry && !p.commandOutcome.Deferred && !p.queueThreadGone() &&
		(p.nativeOutcome == nil || !p.nativeOutcome.NoRetry)
}

func (p wakePlan) queueThreadGone() bool {
	return p.queueOutcome != nil && p.queueOutcome.ThreadState == "gone"
}

func (p wakePlan) deliverySettled(socket, written bool) bool {
	return (socket && !written) || (p.nativeOutcome != nil && p.nativeOutcome.Settled)
}

func (p wakePlan) queuedDelivery() bool {
	return queues(p) && (p.nativeOutcome == nil || !p.nativeOutcome.OK)
}

func (e *Engine) armFailedWakePlan(p wakePlan, n int, agent string, keys []string, after time.Duration) {
	if p.retryAllowed() {
		e.armFirstFailedWake(n, agent, keys, after)
	}
}

// Classification only; ownership and turn state come from the real app IPC
// off the writer. A configured native route must not be held by Dibs busy policy.
func (e *Engine) nativeAppRoute(l *core.Agent) bool {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	return e.nativeAppRouteLocked(l)
}

func (e *Engine) nativeAppRouteLocked(l *core.Agent) bool {
	legacyRemoteApp := archivedApp(l) && legacyCodexAppPossible(l) && e.remoteHostOf(l) != ""
	if surfaceOf(l) != harnessenv.ChatGPTApp && !legacyRemoteApp {
		return false
	}
	if e.remoteHostOf(l) != "" {
		e.hostWakes.mu.Lock()
		defer e.hostWakes.mu.Unlock()
		b := e.hostWakes.bridges[e.remoteHostOf(l)]
		return b != nil && b.nativeApp && wakeHarness(l) == "codex" &&
			(surfaceOf(l) == harnessenv.ChatGPTApp || b.nativeOnly)
	}
	c := e.commandFor(l)
	return wakeexec.NativeQueueTemplate(c.argv)
}

func (e *Engine) tryNativeWake(plan *wakePlan, agent string) (bool, bool) {
	if !wakeexec.NativeEligible(plan.surface, plan.fields, plan.argv) {
		return false, false
	}
	held, probe := e.nativeTurnOffer(*plan)
	if held {
		e.recordHeldNativeWake(plan, agent)
		return false, true
	}
	if probe != 0 {
		plan.commandKeys = []string{plan.nativeProbeKey()}
	}
	started := time.Now()
	var offeredAt time.Time
	fresh := func() (string, error) {
		notice, err := e.nativePlanNotice(*plan, agent)
		if notice != "" && err == nil {
			offeredAt = time.Now()
		}
		return notice, err
	}
	out, handled := wakeexec.TryNativeContext(e.deliveryContext(), plan.surface, plan.fields, plan.argv, fresh)
	if probe != 0 && handled && !out.OK {
		out.NoRetry = true
	}
	if plan.nativeOutcome != nil {
		*plan.nativeOutcome = out
	}
	if !handled {
		return e.beforeColdRoute(*plan, agent, out, fresh)
	}
	if out.OK && nativeDeliveryAccepted(out.Disposition) {
		e.noteNativeAcceptance(*plan, out, probe, started, offeredAt)
	}
	e.noteQueueObservation(*plan, agent, wakeexec.QueueObservation{
		Admission: "not_queued", Pending: "not_applicable", At: time.Now().UTC(), Delivery: out.Disposition,
	})
	return out.OK, true
}

func (e *Engine) recordHeldNativeWake(plan *wakePlan, agent string) {
	const disposition = "turns_ended"
	if plan.nativeOutcome != nil {
		*plan.nativeOutcome = wakeexec.NativeOutcome{Disposition: disposition, NoRetry: true}
	}
	e.noteQueueObservation(*plan, agent, wakeexec.QueueObservation{
		Admission: "not_queued", Pending: "not_applicable", At: time.Now().UTC(), Delivery: disposition,
	})
}

func (e *Engine) noteNativeAcceptance(
	plan wakePlan, out wakeexec.NativeOutcome, probe uint64, started, offeredAt time.Time,
) {
	if err := harnessenv.ConfirmChatGPTBinding(plan.thread); err != nil {
		slog.Warn("could not clear confirmed app binding", "agent", plan.agent, "err", err)
	}
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[plan.agent]
		if l == nil || l.CreatedSerial != plan.createdSerial || threadIDOf(l) != plan.thread {
			return nil
		}
		delete(e.appBindings, plan.agent)
		if probe != 0 {
			e.noteNativeProbe(plan, probe, out.TurnID, offeredAt)
			return nil // a probe of an alerted thread does not start another page clock
		}
		if watch := e.nativeTurnWatches[plan.agent]; watch.heldAt != 0 && watch.fence == plan.nativeContactFence() {
			return nil // the hold was set while this earlier submission was in flight
		}
		now := time.Now()
		e.noteNativeTurn(plan, out.TurnID, offeredAt, now)
		e.noteAcceptedContact(plan, started, now)
		return nil
	})
}

// beforeColdRoute runs when native input was not sent. handled=true means
// the cold route must not run: the mail is held for the app's return, or the
// freshness fence found nothing owed or could not be read.
func (e *Engine) beforeColdRoute(
	plan wakePlan, agent string, out wakeexec.NativeOutcome, fresh func() (string, error),
) (bool, bool) {
	if e.heldForAppReturn(plan, agent, out) {
		return false, true
	}
	if out.Disposition != "unloaded" && out.Disposition != "native_failed" {
		return false, false
	}
	text, err := fresh()
	if err != nil {
		if plan.nativeOutcome != nil {
			*plan.nativeOutcome = wakeexec.NativeOutcome{Disposition: "not_sent"}
		}
		return false, true
	}
	if text == "" {
		if plan.nativeOutcome != nil {
			*plan.nativeOutcome = wakeexec.NativeOutcome{OK: true, Settled: true}
		}
		return true, true
	}
	return false, false
}

func (e *Engine) nativePlanNotice(plan wakePlan, agent string) (string, error) {
	res, err := e.query(e.wakeContext, func() core.Result {
		if !e.originalNativeWakeOwed(plan, agent) {
			return core.Result{"notice": ""}
		}
		return core.Result{"notice": wakeexec.ComposeNative(plan.fields)}
	})
	text, _ := res["notice"].(string)
	return text, err
}

// Called on the writer, shared by both machine-local and remote fences.
func (e *Engine) originalNativeWakeOwed(plan wakePlan, agent string) bool {
	l := e.state.Agents[agent]
	if l == nil || l.Retired() || l.CreatedSerial != plan.createdSerial || !l.SessionIsCurrent(plan.session) ||
		e.commandEpoch[agent] != plan.commandEpoch {
		return false
	}
	for _, key := range e.freshCommandKeys(agent, time.Now()) {
		for _, captured := range plan.commandKeys {
			if key == captured {
				return true
			}
		}
	}
	return false
}

// appRestarting reads a refused native dial as a restart when the app's
// process is up and that incarnation's return has not settled yet: the cold
// route would open the thread in a window that is coming back, which is the
// switching every ChatGPT restart caused. With the process gone the socket is
// stale; the cold route queues the notice and does not open the thread,
// because opening it would launch an app the person quit.
//
// Restarting is a window, not a state. afterAppReturn settles the incarnation
// whether or not IPC answered, and a socket that still refuses after that is
// the app build itself: ChatGPT 26.1117.31558 ships without the IPC router,
// and reading it as "restarting" held every ChatGPT agent's mail with no end.
func (e *Engine) appRestarting(out wakeexec.NativeOutcome) bool {
	if !out.AppNotListening {
		return false
	}
	epoch, known := currentAppRestartEpoch()
	if !known || epoch == "absent" {
		return false
	}
	// No incarnation observed yet is a daemon starting beside a running app,
	// not a restart: nothing would re-offer mail held now. appReturnPending
	// reads an empty settlement the same way.
	settled, _ := e.appSettled.Load().(string)
	return settled != "" && settled != epoch
}

// heldForAppReturn decides between waiting for the app and the cold route,
// for a native attempt that did not deliver. Held mail is neither queued nor
// opened: the app's return re-offers ordinary background delivery
// (afterAppReturn). The one bounded retry stays armed in case the app never
// comes back as a new process.
// noIPCHint names the build change and what the queue route confirms.
const noIPCHint = "ChatGPT 26.1117 and later ship without the IPC router; the queue route " +
	"never navigates and confirms a queued item, not a turn"

func (e *Engine) heldForAppReturn(plan wakePlan, agent string, out wakeexec.NativeOutcome) bool {
	if e.appRestarting(out) || (out.Disposition == "unloaded" && e.appReturnPending()) {
		slog.Info("the ChatGPT app is restarting; holding this agent's mail until IPC is ready",
			"agent", agent, "hint", "delivery is reconsidered on return through the ordinary background route")
		e.noteQueueObservation(plan, agent, wakeexec.QueueObservation{
			Admission: "not_queued", Pending: "not_applicable", At: time.Now().UTC(), Delivery: "held_app_restarting",
		})
		return true
	}
	if out.AppNotListening {
		if epoch, known := currentAppRestartEpoch(); known && epoch != "absent" {
			slog.Info("the running ChatGPT app refuses native delivery; using the background queue route",
				"agent", agent, "hint", noIPCHint)
		} else {
			slog.Info("the ChatGPT app is not running; using the cold route, which queues without launching it", "agent", agent)
		}
	}
	return false
}
