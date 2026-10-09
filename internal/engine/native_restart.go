// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func (e *Engine) tryNativeRestart(ctx context.Context, epoch string, plan wakePlan) (bool, bool) {
	fresh := func() (string, error) {
		freshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		current, known := currentAppRestartEpoch()
		if !known || current != epoch {
			return "", nil
		}
		res, err := e.query(freshCtx, func() core.Result {
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
	out, handled := wakeexec.TryNative(plan.surface, plan.fields, plan.argv, fresh)
	if plan.nativeOutcome != nil {
		*plan.nativeOutcome = out
	}
	return out.OK, handled
}
