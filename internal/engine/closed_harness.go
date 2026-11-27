// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func (e *Engine) closedClaudeBinding(l *core.Agent) (appBindingAlert, bool) {
	alert := e.appBindings[l.ID]
	return alert, alert.reason == "waiting-thread-open" && alert.created == l.CreatedSerial &&
		alert.thread == threadIDOf(l) && alert.surface == surfaceOf(l) && e.ownsHost(l) &&
		!e.lastEvidenceOf(l).After(alert.evidence)
}

func (e *Engine) closedClaudeSendNote(l *core.Agent, m *core.Message, alert appBindingAlert) string {
	note := "Stored in " + l.ID + "'s mailbox. Claude Desktop has no available background input route. " +
		"A started turn and a mail read are unconfirmed."
	if argv := harnessenv.ClaudeOpenArgv(alert.appSession); len(argv) != 0 {
		note += " Claude Desktop session " + alert.appSession + ": " + argv[len(argv)-1] + "."
	}
	if c := e.state.Contacts[m.ContactEscalatedAt]; c != nil {
		switch e.contactPostingState(c) {
		case "posted":
			if c.OldestSerial == m.Serial {
				note += fmt.Sprintf(" Contact alert #%d was posted for the person.", c.Serial)
			} else {
				note += fmt.Sprintf(" Your mail joined contact alert #%d, which was posted for the person.", c.Serial)
			}
		case "failed":
			note += fmt.Sprintf(" Contact alert #%d was recorded; notification posting failed.", c.Serial)
		case "resolved":
			note += fmt.Sprintf(" Contact alert #%d is resolved.", c.Serial)
		default:
			note += fmt.Sprintf(" Contact alert #%d was recorded for the person; notification posting is unconfirmed.", c.Serial)
		}
	}
	return note
}

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
		"An unanswered question or request can expire. " +
		"For time-sensitive work, choose another agent with a reachable harness."
	if surfaceOf(l) == harnessenv.ClaudeDesktop {
		note += " The Claude Desktop session and contact alert status are reported after the background route check."
	} else {
		note += " Unconfirmed delivery may raise a contact alert to the person."
		if thread := threadIDOf(l); thread != "" {
			note += " Original session: " + thread + "."
		}
	}
	if l.Status == core.StatusArchived {
		note += " Retention has ARCHIVED its row; it must register with the same nonce to recover its mailbox."
	}
	return note
}
