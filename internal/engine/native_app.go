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

func (p wakePlan) retryAllowed() bool { return p.nativeOutcome == nil || !p.nativeOutcome.NoRetry }

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
	if surfaceOf(l) != harnessenv.ChatGPTApp {
		return false
	}
	if e.remoteHostOf(l) != "" {
		e.hostWakes.mu.Lock()
		defer e.hostWakes.mu.Unlock()
		b := e.hostWakes.bridges[e.remoteHostOf(l)]
		return b != nil && b.nativeApp && wakeHarness(l) == "codex"
	}
	c := e.commandFor(l)
	return wakeexec.NativeQueueTemplate(c.argv)
}

func (e *Engine) tryNativeWake(plan wakePlan, agent string) (bool, bool) {
	fresh := func() (string, error) { return e.nativePlanNotice(plan, agent) }
	out, handled := wakeexec.TryNative(plan.surface, plan.fields, plan.argv, fresh)
	if plan.nativeOutcome != nil {
		*plan.nativeOutcome = out
	}
	if !handled {
		return e.beforeColdRoute(plan, agent, out, fresh)
	}
	e.noteQueueObservation(plan, agent, wakeexec.QueueObservation{
		Admission: "not_queued", Pending: "not_applicable", At: time.Now().UTC(), Delivery: out.Disposition,
	})
	return out.OK, true
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
// process is up: the cold route would open the thread in a window that is
// coming back, which is the switching every ChatGPT restart caused. With the
// process gone the socket is stale, and the cold route is what launches it.
func appRestarting(out wakeexec.NativeOutcome) bool {
	if !out.AppNotListening {
		return false
	}
	epoch, known := currentAppRestartEpoch()
	return known && epoch != "absent"
}

// heldForAppReturn decides between waiting for the app and the cold route,
// for a native attempt that did not deliver. Held mail is neither queued nor
// opened: the app's return loads the thread and redelivers it
// (afterAppReturn). The one bounded retry stays armed in case the app never
// comes back as a new process.
func (e *Engine) heldForAppReturn(plan wakePlan, agent string, out wakeexec.NativeOutcome) bool {
	if appRestarting(out) || (out.Disposition == "unloaded" && e.appReturnPending()) {
		slog.Info("the ChatGPT app is restarting; holding this agent's mail until its thread is loaded",
			"agent", agent, "hint", "delivered natively once the app is back; nothing is opened meanwhile")
		e.noteQueueObservation(plan, agent, wakeexec.QueueObservation{
			Admission: "not_queued", Pending: "not_applicable", At: time.Now().UTC(), Delivery: "held_app_restarting",
		})
		return true
	}
	if out.AppNotListening {
		slog.Info("the ChatGPT app is not running; using the cold route, which launches it", "agent", agent)
	}
	return false
}
