// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

// Absence of a route or a dormant status alone proves no harness closure. Use
// the same positive process evidence the board's proc_alive projection uses.
func (e *Engine) closedHarnessNote(l *core.Agent, socket bool) string {
	if socket || l.PID <= 1 || e.prober == nil || !e.ownsHost(l) ||
		e.prober.Alive(l.PID) || e.sessionMovedProcess(l) ||
		e.processContact[l.ID] == recordedProcess(l) {
		return ""
	}
	note := "Stored in " + l.ID + "'s mailbox; harness closed. No started turn or read mail is confirmed. " +
		"The mail waits for the original harness to start or a supported background wake to reach it. " +
		"An unanswered question or request can expire, and unconfirmed delivery may raise a contact alert to the person. " +
		"For time-sensitive work, choose another agent with a reachable harness."
	if thread := threadIDOf(l); thread != "" {
		note += " Original session: " + thread + "."
	}
	if l.Status == core.StatusArchived {
		note += " Retention has ARCHIVED its row; it must register with the same nonce to recover its mailbox."
	}
	return note
}
