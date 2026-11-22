// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

const bindingRemedy = "Start a local app thread and register with this agent's saved nonce to recover it. " +
	"Without the nonce, register and send the coordinator the adoption request registration returns " +
	"(send(to:\"coordinator\", type:\"request\", adopt:\"<old id>\")). " +
	"Or call bind_session(token, session_id:<that local thread's actual ID>) if you hold that agent's current token. " +
	"Keep the returned token and nonce."

const cloudBindingReason = "Bound to a cloud-environment Codex thread; Dibs can only wake local app threads."

const missingAppThreadReason = "No local ChatGPT app thread is known for this agent."

// Send and board views share the recovery advice for an unusable local app
// binding. A separate working socket can still reach the session.
func (e *Engine) unavailableAppBindingAdvice(l *core.Agent) string {
	if l.Retired() || surfaceOf(l) != harnessenv.ChatGPTApp || invitedAgent(l) || !e.ownsHost(l) {
		return ""
	}
	reason := missingAppThreadReason
	if threadIDOf(l) != "" {
		reason = e.bindingRecoveryReason(l)
		if reason == "" {
			return ""
		}
		reason = "The bound app thread is gone. " + reason
	}
	if socket, _ := e.sendNoteSocketRoute(l); socket {
		return ""
	}
	return reason + " " + bindingRemedy
}

func (e *Engine) unavailableAppBindingSendNote(l *core.Agent) string {
	if advice := e.unavailableAppBindingAdvice(l); advice != "" {
		return "Stored in the mailbox for " + l.ID + ". " + advice
	}
	return ""
}

// On the writer: a cloud surface is explicit provenance; gone is the queue's
// refusal for this exact local binding. Missing files alone establish neither.
func (e *Engine) bindingNeedsRecovery(l *core.Agent) bool {
	return e.bindingRecoveryReason(l) != ""
}

func (e *Engine) bindingRecoveryReason(l *core.Agent) string {
	if surfaceOf(l) == harnessenv.ChatGPTCloudExec {
		return cloudBindingReason
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	return e.queueUnavailableReasonLocked(l)
}

type bindingAdviceTarget struct {
	created    uint64
	thread     string
	goneReason string
	cloud      bool
}

// Advisory reply evidence, outside the writer: ownership discovery sends no
// input and never opens the app. Neither absent files nor a failed probe proves
// a thread is gone. A queue refusal or verified exec provenance can establish it.
func (e *Engine) addBindingAdvice(ctx context.Context, op *core.Op, res core.Result) {
	if op == nil || res == nil || res["superseded"] == true {
		return
	}
	switch op.Kind {
	case core.OpRegister, core.OpResume, core.OpBindSession:
	default:
		return
	}
	token := op.Token
	if v, _ := res["token"].(string); v != "" {
		token = v
	}
	var before bindingAdviceTarget
	if _, err := e.query(ctx, func() core.Result {
		before = e.bindingAdviceTarget(token)
		return nil
	}); err != nil || before.thread == "" {
		return
	}
	if bindingEvidenceVerified(ctx, before) {
		return
	}
	// Do not attach a probe's result to a replacement binding or incarnation.
	var current bindingAdviceTarget
	if _, err := e.query(ctx, func() core.Result {
		current = e.bindingAdviceTarget(token)
		return nil
	}); err != nil || current.created != before.created || current.thread != before.thread {
		return
	}
	status, message := "unverified", "Registration/binding succeeded, but wake delivery is unverified: "+
		"no local rollout was found and app ownership was not confirmed. This does not prove the thread is gone."
	if current.goneReason != "" {
		status, message = "gone", current.goneReason+" "+
			"No automatic retry will run; mail remains in the mailbox."
	}
	if current.cloud {
		status, message = "unsupported", cloudBindingReason+" Registration/binding succeeded."
	}
	res["binding_warning"] = core.Result{
		"status": status, "thread_id": current.thread, "message": message, "hint": bindingRemedy,
	}
}

func bindingEvidenceVerified(ctx context.Context, target bindingAdviceTarget) bool {
	if target.goneReason != "" || target.cloud {
		return false
	}
	if harnessenv.CodexRolloutPresent(target.thread) {
		return true
	}
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	owned, err := codexipc.Owned(probe, target.thread)
	return err == nil && owned
}

// Called only on the writer. Remote paths and invited pull-only agents cannot
// be checked against this computer's rollout store or app.
func (e *Engine) bindingAdviceTarget(token string) bindingAdviceTarget {
	l := e.state.AgentByToken(token)
	if l.Retired() || !e.ownsHost(l) || invitedAgent(l) ||
		(surfaceOf(l) != harnessenv.ChatGPTApp && surfaceOf(l) != harnessenv.ChatGPTCloudExec) {
		return bindingAdviceTarget{}
	}
	e.wakers.mu.Lock()
	goneReason := e.queueUnavailableReasonLocked(l)
	e.wakers.mu.Unlock()
	return bindingAdviceTarget{l.CreatedSerial, threadIDOf(l), goneReason, surfaceOf(l) == harnessenv.ChatGPTCloudExec}
}
