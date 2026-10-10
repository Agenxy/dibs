// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Send returns before its background command exits, so a configured queue is
// not an acceptance receipt. Describe acceptance only after it was observed.
// Either note replaces the fold's misleading "when it next wakes" diagnosis.
func (e *Engine) appQueueNote(agent *core.Agent) string {
	if view := e.queueWakeView(agent, time.Now()); view != nil {
		return "Stored in the mailbox for " + agent.ID + ". " + queueWakeNote(view)
	}
	return ""
}
