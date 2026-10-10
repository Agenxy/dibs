// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func (e *Engine) tryNativeRestart(ctx context.Context, epoch string, plan wakePlan) (bool, bool) {
	fresh := func() (string, error) { return e.nativeRestartNotice(ctx, epoch, plan) }
	out, handled := wakeexec.TryNative(plan.surface, plan.fields, plan.argv, fresh)
	if !handled && (out.Disposition == "unloaded" || out.Disposition == "native_failed") {
		text, err := fresh()
		if err != nil {
			out, handled = wakeexec.NativeOutcome{Disposition: "not_sent"}, true
		} else if text == "" {
			out, handled = wakeexec.NativeOutcome{OK: true, Settled: true, Disposition: "settled"}, true
		}
	}
	if plan.nativeOutcome != nil {
		*plan.nativeOutcome = out
	}
	return out.OK, handled
}

func (e *Engine) nativeRestartNotice(ctx context.Context, epoch string, plan wakePlan) (string, error) {
	current, known := currentAppRestartEpoch()
	if !known || current != epoch {
		return "", nil
	}
	res, err := e.query(ctx, func() core.Result {
		l := e.state.Agents[plan.agent]
		n, found := e.state.RestartNotices[plan.agent]
		if !found || l == nil || l.Retired() || l.CreatedSerial != plan.createdSerial ||
			n.CreatedSerial != plan.createdSerial || !l.SessionIsCurrent(plan.session) {
			return core.Result{"notice": ""}
		}
		return core.Result{"notice": wakeexec.ComposeNative(plan.fields)}
	})
	text, _ := res["notice"].(string)
	return text, err
}
