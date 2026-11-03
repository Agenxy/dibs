// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

type appBindingAlert struct {
	created        uint64
	thread, reason string
}

func archivedApp(l *core.Agent) bool {
	return l != nil && l.Status == core.StatusArchived &&
		(surfaceOf(l) == harnessenv.ChatGPTApp || surfaceOf(l) == harnessenv.ClaudeDesktop)
}

// Writer-owned, derived metadata. Repeated mail does not add alerts or move
// their observation time. A new incarnation/binding or reattachment clears it.
func (e *Engine) noteAppBinding(l *core.Agent, reason string) {
	if l == nil || l.Retired() {
		return
	}
	if e.appBindings == nil {
		e.appBindings = map[string]appBindingAlert{}
	}
	e.appBindings[l.ID] = appBindingAlert{l.CreatedSerial, threadIDOf(l), reason}
}

func (e *Engine) appNavigationAllowedLocked(agent string, created uint64, thread string) bool {
	l := e.state.Agents[agent]
	if l == nil || l.Retired() || l.CreatedSerial != created || threadIDOf(l) != thread {
		return false
	}
	if l.Status == core.StatusArchived {
		e.noteAppBinding(l, "archived")
		return false
	}
	return true
}

func (e *Engine) appNavigationAllowed(agent string, created uint64, thread string) bool {
	if e.wakeContext == nil {
		return e.appNavigationAllowedLocked(agent, created, thread)
	} // decision-only engines have no worker
	r, err := e.query(e.wakeContext, func() core.Result {
		return core.Result{"allowed": e.appNavigationAllowedLocked(agent, created, thread)}
	})
	return err == nil && r["allowed"] == true
}

// This copy carries a fresh eligibility fence all the way to the actual
// opener, including an open deferred by desktop-lock contention.
func (e *Engine) appShower(agent string, created uint64, thread string) harnessenv.Shower {
	s := *shower
	s.AllowOpen = func(string) bool { return e.appNavigationAllowed(agent, created, thread) }
	return s
}

func (e *Engine) decorateAppBindings(board core.Result) {
	var alerts []core.Result
	for id, alert := range e.appBindings {
		l := e.state.Agents[id]
		if l == nil || l.Retired() || l.CreatedSerial != alert.created || threadIDOf(l) != alert.thread ||
			(alert.reason == "archived" && l.Status != core.StatusArchived) {
			delete(e.appBindings, id)
			continue
		}
		alerts = append(alerts, core.Result{
			"agent": id, "reason": alert.reason,
			"hint": "Mail remains stored. Reattach the same agent from an existing app thread; " +
				"archived bindings never navigate.",
		})
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i]["agent"].(string) < alerts[j]["agent"].(string) })
	if len(alerts) > 0 {
		board["app_binding_alerts"] = alerts
	}
}

func (e *Engine) refreshRemoteNavigation(plan *wakePlan, agent string) bool {
	if e.wakeContext == nil {
		return true
	}
	r, err := e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[agent]
		if l == nil || l.Retired() || l.CreatedSerial != plan.createdSerial || threadIDOf(l) != plan.thread {
			return core.Result{"current": false}
		}
		archived := archivedApp(l)
		if archived {
			e.noteAppBinding(l, "archived")
		}
		return core.Result{"current": true, "archived": archived}
	})
	if err != nil || r["current"] != true {
		return false
	}
	plan.request.NoNavigation = r["archived"] == true
	return true
}

func (e *Engine) restartNavigationAllowed(ctx context.Context, agents []string, thread string) bool {
	r, err := e.query(ctx, func() core.Result {
		for _, id := range agents {
			l := e.state.Agents[id]
			if l == nil || threadIDOf(l) != thread {
				continue
			}
			if e.appNavigationAllowedLocked(id, l.CreatedSerial, thread) && len(e.freshCommandKeys(id, time.Now())) > 0 {
				return core.Result{"allowed": true}
			}
		}
		return core.Result{"allowed": false}
	})
	return err == nil && r["allowed"] == true
}
