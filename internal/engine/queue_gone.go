// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Caller holds wakers.mu. A missing rollout describes one local target, not
// the agent forever. New bindings, incarnations and wake configurations re-arm it.
func (e *Engine) queueThreadGoneLocked(l *core.Agent) bool {
	if l == nil || l.Retired() || e.remoteHostOf(l) != "" {
		return false
	}
	r, ok := e.wakers.queueObserved[l.ID]
	return ok && r.createdSerial == l.CreatedSerial && r.thread == threadIDOf(l) &&
		r.observation.ThreadState == "gone"
}

func (e *Engine) recordNonRetryableWake(plan wakePlan) {
	if plan.queueThreadGone() {
		e.reportGoneQueueThread(plan)
	} else {
		e.recordNativeUnknown(plan)
	}
}

func (e *Engine) reportGoneQueueThread(plan wakePlan) {
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[plan.agent]
		e.wakers.mu.Lock()
		current := e.queueThreadGoneLocked(l) && l.CreatedSerial == plan.createdSerial &&
			threadIDOf(l) == plan.thread && e.wakers.queueEpoch == plan.queueEpoch
		if current {
			if timer := e.wakers.deferred[plan.agent]; timer != nil {
				timer.Stop()
			}
			delete(e.wakers.deferred, plan.agent)
			delete(e.wakers.failedCauses, plan.agent)
		}
		e.wakers.mu.Unlock()
		if current {
			e.reportWakeFailureDecision(plan.agent, time.Now())
			e.escalateContact(plan.agent)
		}
		return nil
	})
}
