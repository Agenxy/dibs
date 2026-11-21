// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

type antigravityBinding struct {
	hub          antigravity.Hub
	conversation string
	created      uint64
	epoch        string
}

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

// BindAntigravity records runtime provenance after the app's verified tool
// call binds the current session. It never obtains an identity by guessing a
// conversation from the row's cwd or by taking a process-wide environment ID.
func (e *Engine) BindAntigravity(ctx context.Context, token string, hub antigravity.Hub, conversation string) {
	_, _ = e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if !e.localAntigravity(l) {
			return nil
		}
		if conversation == "" {
			delete(e.antigravity, l.ID)
			return nil
		}
		if l.CurrentSession != conversation || !core.LooksLikeThreadID(conversation) {
			return nil
		}
		if e.antigravity == nil {
			e.antigravity = map[string]antigravityBinding{}
		}
		e.antigravity[l.ID] = antigravityBinding{
			hub: hub, conversation: conversation, created: l.CreatedSerial, epoch: e.commandEpoch[l.ID],
		}
		return nil
	})
}

func (e *Engine) localAntigravity(l *core.Agent) bool {
	return l != nil && !l.Retired() && antigravityHarness(l) && !invitedAgent(l) &&
		l.Agent.HostID != "" && e.remoteHostOf(l) == ""
}

func (e *Engine) antigravityBinding(l *core.Agent) antigravityBinding {
	if !e.localAntigravity(l) {
		return antigravityBinding{}
	}
	binding := e.antigravity[l.ID]
	if binding.created != l.CreatedSerial || binding.conversation != l.CurrentSession ||
		binding.epoch != e.commandEpoch[l.ID] {
		return antigravityBinding{}
	}
	return binding
}

func (e *Engine) runAntigravityWake(plan *wakePlan, agent string) bool {
	ctx := e.deliveryContext()
	// The descriptor, its credentials and the exact conversation are checked
	// outside the writer. Recheck the receiving incarnation and the original
	// mail after that I/O so a move or read can settle the plan before delivery.
	submit, err := prepareAntigravity(ctx, plan.antigravity, plan.thread, plan.cwd)
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
		return core.Result{"owed": binding.hub == plan.antigravity && binding.conversation == plan.thread &&
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
