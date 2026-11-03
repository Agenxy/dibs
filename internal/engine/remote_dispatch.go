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

func (e *Engine) prepareRemoteWake(plan wakePlan, agent string) core.Result {
	var pending pendingWake
	var id uint64
	if e.wakeContext != nil && !e.refreshRemoteNavigationLocked(&plan, agent) {
		return core.Result{"plan": plan}
	}
	if plan.host != "" {
		pending, id = e.enqueueRemoteWake(plan, agent)
		if id == 0 && plan.routeRefreshed {
			e.refuseChangedRouteLocked(&plan, e.state.Agents[agent])
		}
	}
	return core.Result{"ready": true, "plan": plan, "pending": pending, "id": id}
}

// The writer checks the binding and enqueues the nonblocking bridge request
// in one turn. An update cannot slip between a successful check and handoff.
// No command, app probe or wait runs here.
func (e *Engine) refreshRemoteNavigationLocked(plan *wakePlan, agent string) bool {
	l := e.state.Agents[agent]
	if plan.trackOffer && e.noCurrentWakeWork(l) {
		settleWakePlan(plan)
		return false // handled mail must not manufacture a missing-route alert
	}
	if !e.remotePlanCurrent(l, *plan) && !e.rebuildWakeRouteLocked(plan, l) {
		return false
	}
	if plan.trackOffer && !e.refreshCurrentRouteMail(l, plan) {
		return false
	}
	if plan.host != "" {
		plan.request.NoNavigation = archivedApp(l)
		if plan.request.NoNavigation {
			e.noteAppBinding(l, "archived")
		}
	}
	return true
}

func (e *Engine) noCurrentWakeWork(l *core.Agent) bool {
	return l != nil && !l.Retired() && len(e.deliveryKeysAt(l.ID, time.Now())) == 0
}

func (e *Engine) remotePlanCurrent(l *core.Agent, plan wakePlan) bool {
	return l != nil && !l.Retired() && l.CreatedSerial == plan.createdSerial &&
		threadIDOf(l) == plan.thread && e.remoteHostOf(l) == plan.host &&
		surfaceOf(l) == plan.request.Surface && wakeHarness(l) == plan.request.Harness &&
		cwdOf(l) == plan.cwd && l.SessionIsCurrent(plan.session)
}

func (e *Engine) rebuildWakeRouteLocked(plan *wakePlan, l *core.Agent) bool {
	if l == nil || l.Retired() || plan.routeRefreshed {
		e.refuseChangedRouteLocked(plan, l)
		return false
	}
	kind, from := e.oldestBlocking(l.ID)
	e.wakers.mu.Lock()
	cool, configured, ok := e.wakeRoute(l)
	var fresh wakePlan
	if ok {
		fresh, ok = e.buildWakePlan(l, kind, core.Event{Agent: from}, cool, configured, time.Now())
	}
	e.wakers.mu.Unlock()
	if !ok {
		e.refuseChangedRouteLocked(plan, l)
		return false
	}
	fresh.routeRefreshed = true
	fresh.trackOffer, fresh.socketWritten = plan.trackOffer, plan.socketWritten
	// Preserve the outcome observed by value-taking decision callers too.
	if plan.nativeOutcome != nil {
		fresh.nativeOutcome = plan.nativeOutcome
	}
	*plan = fresh
	return true
}

func (e *Engine) refreshCurrentRouteMail(l *core.Agent, plan *wakePlan) bool {
	now := time.Now()
	text := e.freshPlanText(l, *plan, now)
	if text == "" {
		settleWakePlan(plan)
		return false // the mail was already handled; no turn was started
	}
	if plan.host != "" || len(plan.argv) > 0 {
		plan.commandKeys = e.freshCommandKeys(l.ID, now)
	} else {
		plan.notice = text // private digest never enters command argv
	}
	return true
}

func settleWakePlan(plan *wakePlan) {
	if plan.nativeOutcome == nil {
		plan.nativeOutcome = new(wakeexec.NativeOutcome)
	}
	plan.nativeOutcome.Settled = true
}

func (e *Engine) refuseChangedRouteLocked(plan *wakePlan, l *core.Agent) {
	if plan.nativeOutcome == nil {
		plan.nativeOutcome = new(wakeexec.NativeOutcome)
	}
	plan.nativeOutcome.NoRetry = true
	plan.routeRefused = true
	reason := "route_changed"
	if archivedApp(l) {
		reason = "archived"
	}
	e.noteAppBinding(l, reason)
}

// Caller holds the writer (except decision-only fixtures). The host mutex
// also protects a bridge channel from concurrent replacement/close.
func (e *Engine) enqueueRemoteWake(plan wakePlan, agent string) (pendingWake, uint64) {
	hw := &e.hostWakes
	hw.mu.Lock()
	defer hw.mu.Unlock()
	b := hw.bridges[plan.host]
	if b == nil || !b.harnesses[plan.request.Harness] {
		slog.Info("no wake: the agent's host has no bridge attached", "agent", agent, "host", plan.host)
		return pendingWake{}, 0
	}
	if !hostCanReceivePlan(plan, b) {
		if plan.nativeOutcome != nil {
			plan.nativeOutcome.NoRetry = true
		}
		if e.wakeContext != nil {
			e.noteAppBinding(e.state.Agents[agent], "archived")
		}
		return pendingWake{}, 0
	}
	if hw.next == 0 {
		hw.next = uint64(time.Now().UnixNano())
	}
	hw.next++
	req := plan.request
	req.ID = hw.next
	pending := pendingWake{
		host: plan.host, ch: make(chan WakeResult, 1), plan: plan,
		native: b.nativeApp && req.Surface == harnessenv.ChatGPTApp,
	}
	if hw.pending == nil {
		hw.pending = map[uint64]pendingWake{}
	}
	hw.pending[req.ID] = pending
	select {
	case b.ch <- req:
		if pending.native && plan.nativeOutcome != nil {
			*plan.nativeOutcome = wakeexec.NativeOutcome{NoRetry: true, Disposition: "unknown"}
		}
		return pending, req.ID
	default:
		delete(hw.pending, req.ID)
		slog.Info("no wake: the host's bridge is not reading its requests", "agent", agent, "host", plan.host)
		return pendingWake{}, 0
	}
}
