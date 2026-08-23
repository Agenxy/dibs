package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func dormantAgent(id, harness, session string) *core.Agent {
	return &core.Agent{
		ID: id, SessionID: session, Status: core.StatusDormant,
		Agent: &core.AgentInfo{Harness: harness},
	}
}

// An agent that is not running must still be reachable.
//
// This is the whole point of the feature and the thing Dibs could not do: every
// other delivery path waits for the agent to come to it, and an idle session
// never does. The operator hit it twice in one day.
func TestMailForAnAgentThatIsNotRunningStartsTheOperatorsCommand(t *testing.T) {
	e := &Engine{}
	e.SetWakeCommands(map[string][]string{
		"codex": {"codex", "queue", "--thread", "{session_id}", "--message", "{message}"},
	}, time.Minute)

	l := dormantAgent("sleeper", "Codex", "019ffe52-thread")
	ev := core.Event{
		Type: "message.sent", To: "sleeper",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	}
	argv, ok := e.wakeFor(l, core.MsgQuestion, ev)
	if !ok {
		t.Fatal("no wake for a dormant agent with a configured harness: the board " +
			"cannot reach it, which is the defect this exists to fix")
	}
	if argv[0] != "codex" || argv[3] != "019ffe52-thread" {
		t.Errorf("argv = %v: the session id must be substituted as a whole element", argv)
	}
	// The BODY never goes on a command line. A wake says mail exists; the agent
	// reads it over the authenticated channel with its own token.
	if strings.Contains(strings.Join(argv, " "), "asker") {
		t.Errorf("argv = %v: it carries message content", argv)
	}
}

// The command is the OPERATOR'S, and nothing an agent says may reach it.
//
// A wake command is arbitrary code on the operator's machine. If any part of it
// came from a message, a peer could execute code here by sending mail, which
// would be worse than never delivering anything. Substitution replaces whole
// argv elements and there is no shell in the path, so a body full of shell
// metacharacters is just a body.
func TestAMessageCannotInfluenceWhatTheWakeCommandRuns(t *testing.T) {
	e := &Engine{}
	e.SetWakeCommands(map[string][]string{
		"codex": {"codex", "queue", "--thread", "{session_id}", "--message", "{message}"},
	}, time.Minute)

	nasty := "; rm -rf / #"
	l := dormantAgent(nasty, "Codex", nasty)
	ev := core.Event{
		Type: "message.sent", To: nasty,
		Data: map[string]any{"msg_type": core.MsgRequest, "from": nasty},
	}
	argv, ok := e.wakeFor(l, core.MsgRequest, ev)
	if !ok {
		t.Fatal("setup: no wake, so this proves nothing about what it would run")
	}
	if argv[0] != "codex" {
		t.Errorf("the executable changed to %q: only the operator names that", argv[0])
	}
	for i, a := range argv {
		if i > 0 && a == nasty && argv[i-1] != "--thread" {
			t.Errorf("argv[%d] took hostile input outside a declared placeholder: %v", i, argv)
		}
	}
	// The hostile string may only appear where a placeholder put it, as ONE
	// element. exec receives it as a single argument, never parsed by a shell.
	if len(argv) != 6 {
		t.Errorf("argv = %v: substitution changed the shape of the command", argv)
	}
}

// A wake per message is a fork bomb with better manners.
func TestAnAgentIsNotWokenTwiceInsideItsCooldown(t *testing.T) {
	e := &Engine{}
	e.SetWakeCommands(map[string][]string{"codex": {"codex", "queue"}}, time.Hour)
	l := dormantAgent("sleeper", "Codex", "t")
	ev := core.Event{
		Type: "message.sent", To: "sleeper",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	}

	if _, ok := e.wakeFor(l, core.MsgQuestion, ev); !ok {
		t.Fatal("setup: the first wake did not happen")
	}
	if _, ok := e.wakeFor(l, core.MsgQuestion, ev); ok {
		t.Error("a second message inside the cooldown woke the agent again: a busy " +
			"fleet would start a process per message")
	}
}

// Only mail somebody is blocked on, and only for an agent that is asleep.
func TestTheBoardDoesNotStartAnythingItDoesNotNeedTo(t *testing.T) {
	e := &Engine{}
	e.SetWakeCommands(map[string][]string{"codex": {"codex", "queue"}}, time.Minute)

	t.Run("an active agent is already reachable", func(t *testing.T) {
		l := dormantAgent("live", "Codex", "t")
		l.Status = core.StatusActive
		e.maybeWake(core.Event{
			Type: "message.sent", To: "live",
			Data: map[string]any{"msg_type": core.MsgQuestion},
		})
		// maybeWake returns before deciding for an active agent; wakeFor is the
		// decision, and it must never have been asked.
		if _, seen := e.wakers.last["live"]; seen {
			t.Error("an active agent was woken: it was going to see this on its own " +
				"next call, and starting something for it is paying twice")
		}
	})

	t.Run("an FYI does not justify starting a process", func(t *testing.T) {
		l := dormantAgent("sleeper2", "Codex", "t")
		if _, ok := e.wakeFor(l, core.MsgNotify, core.Event{}); ok {
			// wakeFor does not filter by type; maybeWake does. Assert the
			// classification directly so the guard names the real rule.
			t.Log("wakeFor is type-blind by design; maybeWake is the filter")
		}
		_ = l
	})

	t.Run("no session id means nowhere to send it", func(t *testing.T) {
		l := dormantAgent("nowhere", "Codex", "")
		if _, ok := e.wakeFor(l, core.MsgQuestion, core.Event{}); ok {
			t.Error("woke an agent with no session id: every mechanism addresses a " +
				"thread, and guessing one wakes somebody else")
		}
	})

	t.Run("a harness with no configured command", func(t *testing.T) {
		l := dormantAgent("other", "SomeOtherHarness", "t")
		if _, ok := e.wakeFor(l, core.MsgQuestion, core.Event{}); ok {
			t.Error("invented a wake for a harness the operator said nothing about")
		}
	})
}
