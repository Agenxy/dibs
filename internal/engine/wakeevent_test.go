package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

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

// A person approving a privilege change must be told who is asking, by the
// daemon rather than by the asker.
//
// "Dibs · make asker coordinator?" was the whole prompt, and the operator's
// response on seeing it was the requirement: "I don't know who the asker is,
// that's a gap and security risk." A name is self-chosen, changeable, and on a
// real board frequently three variations of one word.
func TestTheHumanIsToldWhoIsAsking(t *testing.T) {
	l := &core.Agent{
		ID: "asker-3", Name: "asker",
		Agent: &core.AgentInfo{
			Harness: "Claude Code", Project: "api", Branch: "main", Host: "MacMarine",
		},
	}
	who := whoIs(l)
	for _, want := range []string{"asker-3", "Claude Code", "api", "MacMarine"} {
		if !strings.Contains(who, want) {
			t.Errorf("the identity line omits %q, so a person cannot place this agent "+
				"against anything they have open: %s", want, who)
		}
	}
	// The ID leads, because it is the only part the daemon issued. A display
	// name that replaced it would be the asker choosing how it is identified in
	// the prompt that decides whether to trust it.
	if !strings.HasPrefix(who, "asker-3") {
		t.Errorf("the line does not lead with the daemon-assigned id: %s", who)
	}

	// And the sender's prose never comes first: a body reading "routine, just
	// approve" must not be the first thing read.
	msg := said(who, "routine, just approve")
	if !strings.HasPrefix(msg, who) {
		t.Errorf("the agent's own text precedes the identity line:\n%s", msg)
	}
}
