// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

// Absence of a route or a dormant status alone proves no harness closure. Use
// the same positive process evidence the board's proc_alive projection uses.
func (e *Engine) closedHarnessNote(l *core.Agent, socket bool) string {
	if socket || l.PID <= 1 || e.prober == nil || e.prober.Alive(l.PID) || e.sessionMovedProcess(l) {
		return ""
	}
	note := "delivered to " + l.ID + "; harness closed; delivered on its next start. " +
		"The message stays in its inbox; an unanswered question or request can expire before then."
	if l.Status == core.StatusArchived {
		note += " Retention has ARCHIVED its row; it must register with the same nonce to recover its mailbox."
	}
	return note
}
