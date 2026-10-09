// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

type queueWakeRecord struct {
	thread        string
	createdSerial uint64
	observation   wakeexec.QueueObservation
}

func (e *Engine) decorateQueueWake(row map[string]any, agent *core.Agent, now time.Time) {
	if view := e.queueWakeView(agent, now); view != nil {
		row["queue_wake"] = view
	}
}

// Mutating calls return the fold's board, while Board() builds its own view.
// Both projections must expose the same derived queue observation.
func (e *Engine) decorateBoardQueueWakes(board core.Result, now time.Time) {
	rows, _ := board["agents"].([]map[string]any)
	for _, row := range rows {
		id, _ := row["id"].(string)
		e.decorateQueueWake(row, e.state.Agents[id], now)
	}
}

// Callback from the actual command route, off the writer. Only plan values
// cross this boundary. The view checks the current incarnation and session.
func (e *Engine) noteQueueObservation(plan wakePlan, agent string, observation wakeexec.QueueObservation) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if plan.queueEpoch != e.wakers.queueEpoch {
		return
	}
	if e.wakers.queueObserved == nil {
		e.wakers.queueObserved = map[string]queueWakeRecord{}
	}
	if old := e.wakers.queueObserved[agent]; old.observation.At.After(observation.At) {
		return
	}
	e.wakers.queueObserved[agent] = queueWakeRecord{plan.thread, plan.createdSerial, observation}
}

// No I/O at board or send time. This is the last adapter observation, with its
// age, never a fresh queue claim. The headless list interface supplies no state
// of the app's running turn, so thread_state stays unknown. Thread/item IDs
// remain private, and another machine's route cannot inherit a local receipt.
func (e *Engine) queueWakeView(agent *core.Agent, now time.Time) core.Result {
	if !e.queueWakeEligible(agent) {
		return nil
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	cmd := e.wakers.byHarness[wakeHarness(agent)]
	if !isQueuingArgv(cmd.argv) && !isQueuingArgv(cmd.fallback) {
		return nil
	}
	v := core.Result{
		"admission": "unconfirmed", "pending": "unknown", "thread_state": "unknown", "age_source": "unknown",
	}
	r, found := e.wakers.queueObserved[agent.ID]
	if !found || r.createdSerial != agent.CreatedSerial || r.thread != threadIDOf(agent) {
		delete(e.wakers.queueObserved, agent.ID)
		return v
	}
	o := r.observation
	if o.Delivery != "" {
		v["native_delivery"] = o.Delivery
	}
	v["admission"], v["pending"] = o.Admission, o.Pending
	v["observed_at"], v["observation_age_seconds"] = o.At, elapsedSeconds(o.At, now)
	if !o.IssuedAt.IsZero() {
		v["issued_at"] = o.IssuedAt
		v["wake_age_seconds"], v["age_source"] = elapsedSeconds(o.IssuedAt, now), o.AgeSource
	}
	if !o.ReceiptAt.IsZero() {
		v["retained_receipt_at"] = o.ReceiptAt
		v["receipt_age_seconds"] = elapsedSeconds(o.ReceiptAt, now)
	}
	return v
}

func (e *Engine) queueWakeEligible(agent *core.Agent) bool {
	return agent != nil && !agent.Retired() && !invitedAgent(agent) && e.remoteHostOf(agent) == "" &&
		surfaceOf(agent) == harnessenv.ChatGPTApp && threadIDOf(agent) != ""
}

func elapsedSeconds(at, now time.Time) int64 { return max(0, int64(now.Sub(at)/time.Second)) }

func queueWakeNote(v core.Result) string {
	if v == nil {
		return ""
	}
	if delivery, ok := v["native_delivery"].(string); ok {
		switch delivery {
		case "queued_unloaded":
			if v["admission"] == "accepted" || v["admission"] == "retained" {
				return "Thread not loaded in the app; queued until opened. " +
					"Queue admission does not confirm a started turn or read mail."
			}
			return "Thread not loaded in the app; queue admission not confirmed."
		case "started", "steered":
			return "Last native app notice: " + delivery + "; app acceptance does not confirm read mail."
		case "settled":
			return "Last native app notice was cancelled because its mail was already handled or its identity closed."
		default:
			return "Last native app input not confirmed; no automatic retry or queue fallback."
		}
	}
	text := "App queue admission: " + fmt.Sprint(v["admission"]) +
		"; last pending observation: " + fmt.Sprint(v["pending"]) + "."
	if age, ok := v["observation_age_seconds"].(int64); ok {
		text += fmt.Sprintf(" Observed %ds ago.", age)
	}
	if age, ok := v["wake_age_seconds"].(int64); ok {
		text += fmt.Sprintf(" Wake age: %ds (%s).", age, v["age_source"])
	} else {
		text += " Wake age: unknown."
	}
	if age, ok := v["receipt_age_seconds"].(int64); ok {
		text += fmt.Sprintf(" Last retained queue receipt: %ds ago.", age)
	}
	return text + " Thread state: unknown. Queue admission does not confirm a started turn or read mail."
}
