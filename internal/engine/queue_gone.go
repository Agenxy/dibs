// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
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
	if plan.routeRefused {
		return // known refusal before handoff; the binding alert retains the mail
	}
	if out := plan.nativeOutcome; out != nil && (out.Disposition == "turns_ended" || out.Disposition == "not_sent") {
		return // no submission. Leave the mail eligible when the thread recovers.
	}
	switch {
	case plan.queueThreadGone():
		e.reportGoneQueueThread(plan)
	case plan.commandOutcome.NoRetry,
		plan.nativeOutcome != nil && plan.nativeOutcome.Disposition == "command_unknown":
		e.recordCommandUnknown(plan)
	default:
		e.recordNativeUnknown(plan)
	}
}

func (e *Engine) reportGoneQueueThread(plan wakePlan) {
	_, reporter, err := e.dibsAgent(e.wakeContext)
	if err != nil {
		slog.Warn("could not prepare missing-thread mailbox warnings", "agent", plan.agent,
			"err", err, "hint", "check this agent's binding and pending mail with dibs doctor")
	}
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
			if reporter != "" {
				e.mailGoneBinding(l, reporter, plan, time.Now())
			} else {
				e.reportWakeFailureDecision(plan.agent, time.Now())
			}
			e.escalateContact(plan.agent)
		}
		return nil
	})
}

// Ordinary daemon-authored mail reaches both parties through the same durable
// mailbox as their work. A transient notice tied to an unanswered envelope is
// filtered from inline outcomes; that cannot be the only failure report.
// The writer rechecks the attempted binding before sending any warnings.
func (e *Engine) mailGoneBinding(l *core.Agent, reporter string, plan wakePlan, now time.Time) {
	parties := map[string]bool{l.ID: true}
	for _, m := range e.state.Inbox(l.ID) {
		if m.State == core.MsgStatePending && !m.Consumed && m.From != "" && m.From != e.dibsRowLocked() {
			parties[m.From] = true
		}
	}
	ids := make([]string, 0, len(parties))
	for id := range parties {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		body := fmt.Sprintf("The board cannot wake %q: the app queue reported no rollout found for its bound thread. "+
			"No automatic retry will run for this binding. Unread mail remains in its mailbox. %s", l.ID, bindingRemedy)
		if id == l.ID {
			body = fmt.Sprintf("Your bound app thread %s could not be woken: the app queue reported no rollout found. "+
				"No automatic retry will run for this binding. Your unread mail remains in your mailbox. %s",
				plan.thread, bindingRemedy)
		}
		if surfaceOf(l) == harnessenv.ChatGPTCloudExec {
			body += " " + cloudBindingReason + " " +
				"Start a local thread and adopt this identity using its saved nonce."
		}
		// Effectively-once within ordinary mail dedup (default 24h/256 ids).
		// Row incarnation and target prevent a rebind inheriting an old warning.
		key := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s", l.ID, l.CreatedSerial, plan.thread, id))
		if _, err := e.exec(&core.Op{
			Kind: core.OpSendMessage, Token: reporter, To: id, MsgType: core.MsgNotify,
			Body: body, OpID: fmt.Sprintf("wake-thread-gone:%x", key),
		}, now); err != nil {
			slog.Warn("could not record missing-thread mailbox warning", "agent", l.ID, "to", id,
				"err", err, "hint", "inspect the binding and mailbox capacity; pending mail is retained")
		}
	}
}
