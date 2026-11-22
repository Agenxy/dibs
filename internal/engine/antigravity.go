// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

var prepareAntigravity = antigravity.Prepare

func antigravityHarness(l *core.Agent) bool {
	harness := wakeHarness(l)
	return harness == antigravity.Harness || harness == antigravity.Client
}

func (e *Engine) isAntigravityOp(op *core.Op) bool {
	if op.Agent != nil && (op.Agent.Harness == antigravity.Harness || op.Agent.Harness == antigravity.Client) {
		return true
	}
	l := e.callerRow(op)
	return l != nil && antigravityHarness(l)
}

const antigravityUnbound = "Antigravity has not supplied a verified conversation on the local MCP bridge. " +
	"Check in from that desktop conversation."

const antigravityRoute = "antigravity_app"

// BindAntigravity records the app's attestation on authenticated local ingress.
// Its proof is the verified current conversation, never a PID or credential.
func (e *Engine) BindAntigravity(ctx context.Context, token, conversation string) {
	_, _ = e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if !e.localAntigravity(l) {
			return nil
		}
		if conversation != "" && (l.CurrentSession != conversation || !core.LooksLikeThreadID(conversation)) {
			return nil
		}
		_, _ = e.applyAndLedger(&core.Op{
			Kind: core.OpAntigravityBinding, Token: token, SessionID: conversation,
		}, time.Now())
		return nil
	})
}

func (e *Engine) localAntigravity(l *core.Agent) bool {
	return l != nil && !l.Retired() && antigravityHarness(l) && !invitedAgent(l) &&
		l.Agent.HostID != "" && e.remoteHostOf(l) == ""
}

func (e *Engine) antigravityBinding(l *core.Agent) uint64 {
	if !e.localAntigravity(l) || !core.LooksLikeThreadID(l.CurrentSession) {
		return 0
	}
	return l.AntigravityBindingSerial
}

func (e *Engine) antigravityPullOnlyNote(l *core.Agent, harness string) (string, bool) {
	if !antigravityHarness(l) {
		return "", false
	}
	if e.antigravityBinding(l) != 0 {
		if e.socketOwnsDelivery(l) {
			return "", false
		}
		return e.antigravityWakeNote(l, nil), true
	}
	if e.localCommandConfigured(harness) || e.mightReachOverSocket(l) {
		return "", false
	}
	return "Stored in the mailbox for " + l.ID + ". " + antigravityUnbound, true
}

// The send is already durable. A scheduled admission and the previous app
// observation are different facts, and the latter must match this binding.
func (e *Engine) antigravityWakeNote(l *core.Agent, m *core.Message) string {
	why := ""
	if m != nil {
		why = e.sendWakeCauseNote(l, m)
	}
	note := why
	if note == "" {
		note = "Stored in the mailbox for " + l.ID + "."
	}
	pending := why == "" && e.antigravityWakeScheduled(l, m)
	if pending {
		note += " Wake delivery to its Antigravity conversation is in progress. App acceptance is unconfirmed."
	}
	last, observed := e.currentWakeDiagnostic(e.wakeDiagnosticTarget(l))
	if observed && last.Route == antigravityRoute {
		return note + antigravityLastWakeNote(last.Outcome)
	}
	if !pending && why == "" {
		note += " Wake status for its verified Antigravity conversation is unknown."
	}
	return note
}

func (e *Engine) antigravityWakeScheduled(l *core.Agent, m *core.Message) bool {
	if !m.UnconsumedEnvelope() || e.wakeBursts[l.ID] == nil {
		return false
	}
	key := "mail:" + noticeKey(l.ID, m.Serial)
	return !e.commandWritten[key] && !e.nativeUnknown[key]
}

func antigravityLastWakeNote(outcome string) string {
	switch outcome {
	case "accepted":
		return " The app accepted the last Antigravity wake. Mail read status is unconfirmed."
	case "not_sent":
		return " The last Antigravity wake stopped before submission."
	case "settled":
		return " The last Antigravity wake was cancelled before submission."
	case "failed":
		return " The last Antigravity wake attempt failed."
	default:
		return " App acceptance of the last Antigravity wake is unknown."
	}
}

func (e *Engine) runAntigravityWake(plan *wakePlan, agent string) bool {
	ctx := e.deliveryContext()
	// The descriptor, its credentials and the exact conversation are checked
	// outside the writer. Recheck the receiving incarnation and the original
	// mail after that I/O so a move or read can settle the plan before delivery.
	submit, err := prepareAntigravity(ctx, plan.thread, plan.cwd)
	if err != nil {
		plan.commandOutcome.NoRetry = true
		plan.nativeOutcome = &wakeexec.NativeOutcome{Disposition: "not_sent"}
		slog.Warn("Antigravity wake refused before submission", "agent", agent, "err", err,
			"hint", "verify the conversation and workspace in the running Antigravity app")
		return false
	}
	res, err := e.query(ctx, func() core.Result {
		l := e.state.Agents[agent]
		binding := e.antigravityBinding(l)
		return core.Result{"owed": binding == plan.antigravity && l.CurrentSession == plan.thread &&
			plan.cwd == cwdOf(l) && e.originalNativeWakeOwed(*plan, agent)}
	})
	if err != nil {
		plan.commandOutcome.NoRetry = true
		return false
	}
	if res["owed"] != true {
		settleWakePlan(plan)
		return true
	}
	if err := submit(ctx, plan.fields.Message); err != nil {
		plan.commandOutcome.NoRetry = true
		slog.Warn("Antigravity wake acceptance is unknown", "agent", agent, "err", err)
		return false
	}
	plan.commandOutcome = wakeexec.CommandOutcome{OK: true}
	slog.Info("Antigravity app accepted a wake notice", "agent", agent)
	return true
}
