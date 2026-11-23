// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

type hookBoardTurn struct {
	created       uint64
	session, host string
	activity      harnessenv.TurnActivity
}

type appBoardTurn struct {
	fence       nativeContactFence
	activity    harnessenv.TurnActivity
	attemptedAt time.Time
}

func (e *Engine) noteBoardHook(l *core.Agent, session, event string) {
	if l == nil || session == "" || !l.SessionIsCurrent(session) || wakeHarness(l) != "claude code" {
		return
	}
	state, at := harnessenv.TurnInProgress, e.hookAlive[l.ID]
	switch event {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest":
	case "Stop":
		state, at = harnessenv.TurnBetween, e.turnEnded[l.ID]
	case "SessionStart", "SessionEnd":
		delete(e.hookBoardTurns, l.ID)
		return
	default:
		return
	}
	if e.hookBoardTurns == nil {
		e.hookBoardTurns = map[string]hookBoardTurn{}
	}
	e.hookBoardTurns[l.ID] = hookBoardTurn{
		created: l.CreatedSerial, session: wakeSessionOf(l), host: e.remoteHostOf(l),
		activity: harnessenv.TurnActivity{State: state, ObservedAt: at},
	}
}

// Wake attempts invalidate earlier Codex observations before submitting input.
// App probes that started before the attempt cannot re-publish that evidence.
func (e *Engine) noteAppBoardWake(plan wakePlan, at time.Time) {
	if plan.surface != harnessenv.ChatGPTApp || plan.host != "" || plan.thread == "" || len(plan.argv) == 0 {
		return
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if plan.queueEpoch != e.wakers.queueEpoch {
		return
	}
	record := e.wakers.appBoardTurnFor(plan.agent, plan.nativeContactFence())
	if record.attemptedAt.Before(at) {
		record.attemptedAt = at
		e.wakers.appBoardTurns[plan.agent] = record
	}
}

// appBoardTurnFor and noteAppBoardTurnLocked require the waker lock.
func (w *wakers) appBoardTurnFor(agent string, fence nativeContactFence) appBoardTurn {
	if w.appBoardTurns == nil {
		w.appBoardTurns = map[string]appBoardTurn{}
	}
	record := w.appBoardTurns[agent]
	if record.fence != fence {
		return appBoardTurn{fence: fence}
	}
	return record
}

func (w *wakers) noteAppBoardTurnLocked(agent string, fence nativeContactFence, activity harnessenv.TurnActivity) {
	if activity.ObservedAt.IsZero() || fence.queueEpoch != w.queueEpoch {
		return
	}
	record := w.appBoardTurnFor(agent, fence)
	if activity.ObservedAt.Before(record.attemptedAt) || activity.ObservedAt.Before(record.activity.ObservedAt) {
		return
	}
	record.activity = activity
	w.appBoardTurns[agent] = record
}

func (e *Engine) decorateBoardTurns(b core.Result) {
	rows, _ := b["agents"].([]map[string]any)
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	for _, row := range rows {
		id, _ := row["id"].(string)
		row["turn"] = harnessenv.TurnUnknown
		if activity := e.boardTurnOf(e.state.Agents[id]); !activity.ObservedAt.IsZero() {
			row["turn"] = activity.State
			if activity.State != harnessenv.TurnUnknown {
				row["turn_observed_at"] = activity.ObservedAt.UTC()
			}
		}
	}
}

// boardTurnOf runs on the writer with the waker lock. It does no app I/O.
func (e *Engine) boardTurnOf(l *core.Agent) harnessenv.TurnActivity {
	if l.Gone() {
		return harnessenv.TurnActivity{}
	}
	if wakeHarness(l) == "claude code" {
		record := e.hookBoardTurns[l.ID]
		if record.created == l.CreatedSerial && record.session == wakeSessionOf(l) && record.host == e.remoteHostOf(l) {
			return record.activity
		}
	}
	if e.queueWakeEligible(l) {
		record := e.wakers.appBoardTurns[l.ID]
		fence := nativeContactFence{
			created: l.CreatedSerial, session: wakeSessionOf(l), thread: threadIDOf(l),
			queueEpoch: e.wakers.queueEpoch, commandEpoch: e.commandEpoch[l.ID],
		}
		if record.fence == fence && !record.activity.ObservedAt.Before(record.attemptedAt) {
			return record.activity
		}
	}
	return harnessenv.TurnActivity{}
}

func (e *Engine) trimBoardTurns() {
	for id := range e.hookBoardTurns {
		if e.state.Agents[id].Gone() {
			delete(e.hookBoardTurns, id)
		}
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	for id := range e.wakers.appBoardTurns {
		if e.state.Agents[id].Gone() {
			delete(e.wakers.appBoardTurns, id)
		}
	}
}
