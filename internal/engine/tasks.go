// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
)

// TrackedMessage returns a copy of a request kept as an MCP task, for
// tasks/get and notifications/tasks. Only a tracked request is a task:
// anything else answers not found, so a task id cannot be used to read an
// ordinary message.
func (e *Engine) TrackedMessage(ctx context.Context, serial uint64) (core.Message, bool, error) {
	var (
		m     core.Message
		found bool
	)
	_, err := e.query(ctx, func() core.Result {
		if msg := e.state.Messages[serial]; msg != nil && msg.Tracked {
			m, found = *msg, true
			if m.State == core.MsgStateQueued {
				m.QueueRank = e.state.QueuePosition(msg)
			}
			m.Progress = append([]core.Progress(nil), msg.Progress...)
			m.Milestones = append([]string(nil), msg.Milestones...)
		}
		return nil
	})
	return m, found, err
}
