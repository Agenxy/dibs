package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// A socket's existence says where a wake could go, not whether this mail
// buys one. Classify the actual recorded message by the writer's shared rule.
func (e *Engine) sendDeliveryNote(l *core.Agent, m *core.Message, now time.Time) string {
	if l == nil {
		return ""
	}
	if l.Retired() || e.isTheHuman(l.ID) || m == nil || m.To != l.ID {
		return e.PullOnlyNote(l)
	}
	if note, remote := e.remotePullOnlyNote(l); remote {
		return note
	}
	self, _ := e.SelfWaking(l.ID)
	if !self && e.localCommandConfigured(wakeHarness(l)) && threadIDOf(l) != "" {
		return e.PullOnlyNote(l)
	}
	if !self && !e.mightReachOverSocket(l) {
		return e.PullOnlyNote(l)
	}
	if !e.socketActionableMessage(m) {
		return "delivered to " + l.ID + "'s mailbox; informational, so no wake was sent: " +
			"it arrives at " + l.ID + "'s next activation (check_in, inbox, SessionStart " +
			"or its next actionable delivery)."
	}
	if e.socketLifecycle(l, now) == "busy" {
		return "delivered to " + l.ID + "'s mailbox; it is mid-turn; " +
			"its wake is deferred until the turn ends."
	}
	return e.PullOnlyNote(l)
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
