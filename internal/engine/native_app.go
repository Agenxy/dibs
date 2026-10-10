// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

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
		if out.Disposition == "unloaded" {
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
		}
		return false, false
	}
	e.noteQueueObservation(plan, agent, wakeexec.QueueObservation{
		Admission: "not_queued", Pending: "not_applicable", At: time.Now().UTC(), Delivery: out.Disposition,
	})
	return out.OK, true
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
