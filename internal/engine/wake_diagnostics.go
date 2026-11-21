// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

type wakeDiagnostic struct {
	Route       string    `json:"route"`
	Outcome     string    `json:"outcome"`
	ObservedAt  time.Time `json:"observed_at"`
	created     uint64
	session     string
	thread      string
	attemptedAt time.Time
}

type wakeDiagnosticTarget struct {
	owner, session, thread, binary string
	incarnation, commandEpoch      uint64
}

// DiagnoseWake reads one binding and, for a configured local app route, its
// read-only app metadata. It leaves activity and delivery receipts alone.
func (e *Engine) DiagnoseWake(ctx context.Context, token, ref string) (core.Result, error) {
	var target wakeDiagnosticTarget
	res, err := e.query(ctx, func() core.Result {
		actor, refused := e.authObserve(token, time.Now())
		if refused != nil {
			return refused
		}
		if !actor.IsCoordinator() {
			return core.Result{"error": core.ErrNotCoordinator}
		}
		op := &core.Op{Kind: core.OpMergeAgents, To: ref}
		if _, err := e.resolveAgentRefs(op); err != nil {
			return core.Result{"error": err}
		}
		agent := e.state.Agents[op.To]
		if agent == nil || agent.Retired() {
			return core.Result{"error": &core.Error{
				Code: "E_NO_AGENT", Msg: "active binding not found",
				Hint: "call check_in and use the agent's id from the board",
			}}
		}
		target = e.wakeDiagnosticTarget(agent)
		return e.localWakeDiagnostics(agent, target)
	})
	if err != nil || target.binary == "" {
		return res, err
	}
	app := wakeexec.DiagnoseAppThread(ctx, target.binary, target.thread)
	return e.query(ctx, func() core.Result {
		actor := e.state.AgentByToken(token)
		if actor == nil || !actor.IsCoordinator() {
			return core.Result{"error": core.ErrNotCoordinator}
		}
		a := e.state.Agents[target.owner]
		if a == nil || a.Retired() || e.wakeDiagnosticTarget(a) != target {
			return core.Result{"error": &core.Error{
				Code: "E_BINDING_CHANGED", Msg: "agent binding changed during the app observation",
				Hint: "call diagnose_wake again for the agent's current binding",
			}}
		}
		res["app"] = app
		return res
	})
}

func (e *Engine) wakeDiagnosticTarget(l *core.Agent) wakeDiagnosticTarget {
	x := wakeDiagnosticTarget{owner: l.ID, incarnation: l.CreatedSerial, session: wakeSessionOf(l), thread: threadIDOf(l)}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	x.commandEpoch = e.wakers.queueEpoch
	if x.thread == "" || !e.queueWakeEligible(l) {
		return x
	}
	cmd := e.commandFor(l)
	if wakeexec.NativeQueueTemplate(cmd.argv) {
		x.binary = cmd.argv[0]
	} else if wakeexec.NativeQueueTemplate(cmd.fallback) {
		x.binary = cmd.fallback[0]
	}
	return x
}

func (e *Engine) localWakeDiagnostics(l *core.Agent, target wakeDiagnosticTarget) core.Result {
	out := core.Result{
		"agent": l.ID, "harness": wakeHarness(l), "surface": surfaceOf(l),
		"session": target.session, "thread": target.thread, "serial": e.state.Serial,
	}
	if surfaceOf(l) == harnessenv.ChatGPTCloudExec {
		out["thread_source"] = "cloud"
	}
	if q := e.queueWakeView(l, time.Now()); q != nil {
		out["last_queue_observation"] = q
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if d, ok := e.wakers.diagnostics[l.ID]; ok && d.created == l.CreatedSerial &&
		d.session == target.session && d.thread == target.thread {
		out["last_attempt"] = d
	}
	return out
}

func diagnosticWakeResult(plan *wakePlan, success bool) (route, outcome string) {
	outcome = "failed"
	if success {
		outcome = "accepted"
	}
	switch {
	case plan.host != "":
		if plan.nativeOutcome != nil && plan.nativeOutcome.Disposition != "" {
			return "host_bridge", plan.nativeOutcome.Disposition
		}
		return "host_bridge", outcome
	case plan.queueOutcome != nil && plan.queueOutcome.ThreadState == "gone":
		return "app_queue", "gone"
	case plan.queueOutcome != nil && plan.queueOutcome.Admission != "":
		return "app_queue", plan.queueOutcome.Admission
	case plan.nativeOutcome != nil && plan.nativeOutcome.Disposition != "":
		return "native_app", plan.nativeOutcome.Disposition
	case len(plan.argv) > 0:
		return "command", outcome
	case plan.socketWritten != nil && *plan.socketWritten:
		return "session_socket", "written"
	default:
		return "session_socket", "unconfirmed"
	}
}

func (e *Engine) noteWakeDiagnostic(plan *wakePlan, agent string, at time.Time, success bool) {
	route, outcome := diagnosticWakeResult(plan, success)
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if len(plan.argv) != 0 && plan.queueEpoch != e.wakers.queueEpoch {
		return
	}
	if e.wakers.diagnostics[agent].attemptedAt.After(at) {
		return
	}
	if e.wakers.diagnostics == nil {
		e.wakers.diagnostics = map[string]wakeDiagnostic{}
	}
	if _, tracked := e.wakers.diagnostics[agent]; !tracked && len(e.wakers.diagnostics) >= 4096 {
		oldest := agent
		for id, record := range e.wakers.diagnostics {
			if oldest == agent || record.attemptedAt.Before(e.wakers.diagnostics[oldest].attemptedAt) {
				oldest = id
			}
		}
		delete(e.wakers.diagnostics, oldest)
	}
	e.wakers.diagnostics[agent] = wakeDiagnostic{
		Route: route, Outcome: outcome, ObservedAt: time.Now().UTC(),
		created: plan.createdSerial, session: plan.session, thread: plan.thread, attemptedAt: at,
	}
}
