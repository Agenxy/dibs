// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// A socket's existence says where a wake could go, not whether this mail
// buys one. Classify the actual recorded message by the writer's shared rule.
func (e *Engine) sendDeliveryNote(l *core.Agent, m *core.Message) string {
	if l == nil {
		return ""
	}
	if l.Retired() || e.isTheHuman(l.ID) || m == nil || m.To != l.ID {
		return e.PullOnlyNote(l)
	}
	if note, remote := e.remotePullOnlyNote(l); remote {
		return note
	}
	socket, self := e.sendNoteSocketRoute(l)
	command := e.localCommandConfigured(wakeHarness(l)) && threadIDOf(l) != ""
	if !socket && !command {
		return e.PullOnlyNote(l)
	}
	if note := e.sendWakeCauseNote(l, m); note != "" {
		return note
	}
	if !socket {
		return e.PullOnlyNote(l)
	}
	why := "its session socket is the selected delivery route"
	if self {
		why = "its in-session bridge owns the socket route"
	}
	if e.socketWritten["mail:"+noticeKey(l.ID, m.Serial)] {
		return "delivered to " + l.ID + "'s mailbox; " + why + "; a best-effort notice was already written " +
			"for this message, so no additional socket frame was sent. This mail remains " +
			"available at Stop or its next activation; the earlier write confirms no receiver acceptance."
	}
	return bestEffortSocketNote(l, wakeHarness(l), string(l.Status), why)
}

func (e *Engine) sendWakeCauseNote(l *core.Agent, m *core.Message) string {
	if e.notifyPresented(l.ID, m) {
		return "delivered to " + l.ID + "'s mailbox; this notify was already presented, so " +
			"no new wake was sent. read_mail retains recently consumed FYIs under the normal retention bounds."
	}
	if !e.socketActionableMessage(m) {
		return "delivered to " + l.ID + "'s mailbox; the operator's wake policy suppresses this wake: " +
			"it arrives at " + l.ID + "'s next activation (check_in, inbox, SessionStart " +
			"or its next actionable delivery)."
	}
	return ""
}

// A claiming bridge has its own socket evidence. Consult daemon discovery
// only when no bridge owns this mailbox, preserving the one-writer route.
func (e *Engine) sendNoteSocketRoute(l *core.Agent) (socket, bridge bool) {
	bridge, _ = e.SelfWaking(l.ID)
	return bridge || e.mightReachOverSocket(l), bridge
}

func bestEffortSocketNote(l *core.Agent, named, state, why string) string {
	lead := why + ", but a session socket for it is open, so a best-effort notice will be tried."
	if harnessenv.NeedsNoWakeCommand(named) {
		lead = why + ", and one is open for it, so a best-effort notice is being handed to it now."
	}
	return "delivered to " + l.ID + ", which is " + state + ". " + lead + " Nothing can confirm " +
		"it arrived: a session in bypassPermissions mode holds peer messages for its human " +
		"unless its settings accept them. If it is held, this arrives when that agent next " +
		"calls inbox or check_in."
}

func sleepingPullOnlyNote(l *core.Agent, named string, configured, socket bool) string {
	why := "nothing on this board can wake " + named
	if configured {
		why = named + " has a wake command, but " + l.ID + " has never supplied " +
			"a harness thread id for it to resume"
	}
	if socket {
		return bestEffortSocketNote(l, named, string(l.Status), commandSideReason(named, configured))
	}
	return "delivered to " + l.ID + ", which is " + string(l.Status) + ", and " +
		why + ". Nothing will start it: this is NOT a message that will be seen " +
		"when it next wakes, because nothing is going to wake it. It waits until " +
		"a person starts that agent again. The message is not lost, and any " +
		"deadline on it will expire unread."
}
