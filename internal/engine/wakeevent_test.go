package engine

import "testing"

// A person typing is not how an agent finds out it has mail.
//
// UserPromptSubmit fires when a HUMAN submits a prompt, and its
// additionalContext is attached to that prompt. Delivering mail there makes the
// operator the transport: a peer's question reaches an agent when, and only
// when, its human happens to say something. That is the failure Dibs exists to
// remove, shipped as a feature.
//
// Reported from a live fleet: "messages to codex/chatgpt desktop agents are
// going into my prompt, this is putting it on my plate to take an action for
// them to notice, agents should be notified directly."
//
// The paths that do not need a person keep working, and this asserts both
// halves: silence on the human's event, delivery on the agent's own.
func TestMailDoesNotRideOnTheHumansPrompt(t *testing.T) {
	e := &Engine{}
	e.SetWakePolicy(WakeAll)

	// Everything maximally in favour of delivering: brand new mail, somebody
	// blocked on it, no stop-hook loop in progress.
	if e.deliverToModel("UserPromptSubmit", true, true, false) {
		t.Error("the mail digest was attached to the human's prompt. An agent that " +
			"learns about a waiting peer only when its operator types is one the " +
			"operator is carrying")
	}

	// SessionStart is the agent's own event: a session beginning should be told
	// what is already waiting for it, and no person has to do anything for that
	// to happen.
	if !e.deliverToModel("SessionStart", true, true, false) {
		t.Error("a starting session was not told what is waiting for it")
	}

	// Stop is the real push, and it still pushes.
	if !e.deliverToModel("Stop", true, true, false) {
		t.Error("Stop stopped delivering: that is the one path that reaches an idle " +
			"agent without anybody typing")
	}
}

// The loop guard and the policy still bind, so removing one event has not
// quietly widened the others.
func TestStopStillRefusesToLoopOrToOverrideThePolicy(t *testing.T) {
	e := &Engine{}
	e.SetWakePolicy(WakeAll)

	if e.deliverToModel("Stop", true, true, true) {
		t.Error("a Stop hook continued a turn that a Stop hook had already " +
			"continued: that is how a wake becomes a loop")
	}
	if e.deliverToModel("Stop", false, false, false) {
		t.Error("stale news woke an agent; each message wakes once")
	}
	e.SetWakePolicy(WakeNone)
	if e.deliverToModel("Stop", true, true, false) {
		t.Error("wake policy `none` still extended a turn")
	}
}
