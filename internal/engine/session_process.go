// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

type claudeProcessKey struct{}

// WithClaudeProcess carries locally verified session evidence to the writer.
// The authenticated operation still selects the actor.
func WithClaudeProcess(ctx context.Context, process harnessenv.ClaudeProcess) context.Context {
	return context.WithValue(ctx, claudeProcessKey{}, process)
}

func claudeProcessFrom(ctx context.Context) harnessenv.ClaudeProcess {
	process, _ := ctx.Value(claudeProcessKey{}).(harnessenv.ClaudeProcess)
	return process
}

// The request already passed the token and rate gates. Recheck process and
// session ownership on the writer before folding either half of the repair.
func (e *Engine) verifiedProcessReplacement(l *core.Agent, process harnessenv.ClaudeProcess) (bool, string) {
	if process.PID <= 1 || process.Start <= 0 || l.PID <= 1 || l.Status != core.StatusActive || !e.ownsHost(l) ||
		l.PID == process.PID || e.prober == nil || e.prober.Alive(l.PID) || e.isTheHuman(l.ID) {
		return false, ""
	}
	clock, ok := e.prober.(StartClock)
	if !ok {
		return false, ""
	}
	start, live := clock.StartMillis(process.PID)
	if !live || start != process.Start {
		return false, ""
	}
	return e.mayClaimSession(process.Session, l.Token, "", e.HostID())
}

func (e *Engine) refreshAuthenticatedProcess(l *core.Agent, process harnessenv.ClaudeProcess, now time.Time) error {
	ok, taken := e.verifiedProcessReplacement(l, process)
	if !ok {
		return nil
	}
	op := &core.Op{
		Kind: core.OpRefreshSessionProcess, Token: l.Token, V7Semantics: true,
		SessionID: process.Session, SessionTakenFrom: taken, PID: process.PID, ProcStart: process.Start,
	}
	if err := e.state.Admit(op); err != nil {
		return err
	}
	_, err := e.applyAndLedger(op, now)
	return err
}
