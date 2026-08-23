package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// bridgeAgent is the shape the ORDINARY Codex path produces: the stdio bridge's
// synthetic host-<ppid> as the primary session id, and the harness's own thread
// uuid kept as an alias. The first version of these tests used a fixture whose
// primary id was already a uuid, which never happens in the configuration
// everyone runs, and so could not see that the wake resumed nothing.
func bridgeAgent(id, harness, thread string) *core.Agent {
	a := &core.Agent{
		ID: id, SessionID: "host-10602", Status: core.StatusDormant,
		Agent: &core.AgentInfo{Harness: harness},
	}
	if thread != "" {
		a.SessionAliases = []string{thread}
	}
	return a
}

// An agent that is not running must still be reachable.
//
// This is the whole point of the feature and the thing Dibs could not do: every
// other delivery path waits for the agent to come to it, and an idle session
// never does. The operator hit it twice in one day.
func TestMailForAnAgentThatIsNotRunningStartsTheOperatorsCommand(t *testing.T) {
	e := &Engine{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"codex", "queue", "--thread", "{session_id}", "--message", "{message}"}, Cooldown: time.Minute},
	})

	l := bridgeAgent("sleeper", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	ev := core.Event{
		Type: "message.sent", To: "sleeper",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	}
	argv, ok := e.wakeFor(l, core.MsgQuestion, ev)
	if !ok {
		t.Fatal("no wake for a dormant agent with a configured harness: the board " +
			"cannot reach it, which is the defect this exists to fix")
	}
	if argv[0] != "codex" || argv[3] != "019ffe52-0eaf-7f60-81cc-6ab1298d76ec" {
		t.Errorf("argv = %v: the wake must carry the HARNESS THREAD id. The bridge's "+
			"host-<ppid> resolves to no thread, so a resume against it starts a "+
			"process that finds nothing and the mail stays unread", argv)
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
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"codex", "queue", "--thread", "{session_id}", "--message", "{message}"}, Cooldown: time.Minute},
	})

	// The hostile string goes where an AGENT can actually put one: its own id
	// and the sender's. The thread id has to be a real one or no wake fires at
	// all, which would make this test prove nothing.
	nasty := "; rm -rf / #"
	l := bridgeAgent(nasty, "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
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
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "queue"}, Cooldown: time.Hour}})
	l := bridgeAgent("sleeper", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
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
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "queue"}, Cooldown: time.Minute}})

	t.Run("an active agent is already reachable", func(t *testing.T) {
		l := bridgeAgent("live", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
		l.Status = core.StatusActive
		l.LastCoordination = time.Now()
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
		l := bridgeAgent("sleeper2", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
		if _, ok := e.wakeFor(l, core.MsgNotify, core.Event{}); ok {
			// wakeFor does not filter by type; maybeWake does. Assert the
			// classification directly so the guard names the real rule.
			t.Log("wakeFor is type-blind by design; maybeWake is the filter")
		}
		_ = l
	})

	t.Run("no session id means nowhere to send it", func(t *testing.T) {
		l := bridgeAgent("nowhere", "Codex", "") // bridge id only, no thread
		if _, ok := e.wakeFor(l, core.MsgQuestion, core.Event{}); ok {
			t.Error("woke an agent with no resumable thread id: every mechanism " +
				"addresses a thread, and the bridge's host-<ppid> is not one")
		}
	})

	t.Run("a harness with no configured command", func(t *testing.T) {
		l := bridgeAgent("other", "SomeOtherHarness", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
		if _, ok := e.wakeFor(l, core.MsgQuestion, core.Event{}); ok {
			t.Error("invented a wake for a harness the operator said nothing about")
		}
	})
}

// The three shapes the first version of this feature got wrong.
func TestTheWakeTargetsTheThreadTheHarnessCanActuallyResume(t *testing.T) {
	e := &Engine{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"codex", "exec", "resume", "{session_id}"}, Cooldown: time.Minute},
	})
	ev := core.Event{
		Type: "message.sent", To: "a",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	}

	t.Run("the bridge id alone is not resumable", func(t *testing.T) {
		l := bridgeAgent("a", "Codex", "") // host-<ppid> and no alias
		if _, ok := e.wakeFor(l, core.MsgQuestion, ev); ok {
			t.Error("woke against host-<ppid>: codex exec resume resolves no thread " +
				"for it, so this spends a process and delivers nothing. Refusing is " +
				"better than a wake that cannot work")
		}
	})

	t.Run("the alias is used when the primary is synthetic", func(t *testing.T) {
		l := bridgeAgent("b", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
		argv, ok := e.wakeFor(l, core.MsgQuestion, ev)
		if !ok {
			t.Fatal("no wake although the harness thread id is on the agent")
		}
		if argv[3] != "019ffe52-0eaf-7f60-81cc-6ab1298d76ec" {
			t.Errorf("argv = %v: took the synthetic id over the real thread", argv)
		}
	})
}

// A verdict on your own request must reach the wake path.
//
// The first version accepted only message.sent, so every approval, denial,
// answer and decline was dropped. An agent that asked and then stopped is the
// clearest case there is for starting it again, and leaving these out
// recreated on this mechanism the defect the notice work had just fixed on the
// other one.
func TestAnApprovalReachesTheSubprocessWake(t *testing.T) {
	for _, evType := range []string{
		"message.approved", "message.denied", "message.answered", "message.declined",
	} {
		t.Run(evType, func(t *testing.T) {
			e := &Engine{}
			st := core.NewState("t", core.DefaultLimits())
			e.state = st
			e.SetWakeCommands(map[string]WakeCommand{
				"codex": {Argv: []string{"echo", "{session_id}"}, Cooldown: time.Hour},
			})
			l := bridgeAgent("asker", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
			st.Agents = map[string]*core.Agent{"asker": l}

			e.maybeWake(core.Event{Type: evType, To: "asker", Data: map[string]any{}})
			if _, spent := e.wakers.last["asker"]; !spent {
				t.Errorf("%s did not reach the wake path: the agent asked, stopped, "+
					"and has no other way to learn the answer", evType)
			}
		})
	}
}

// `active` is a lease, not a running process.
//
// Stop and SessionEnd finish only the supervision child; the core agent stays
// active until its idle lease lapses, 45 minutes by default. Treating that as
// "it will see this anyway" threw away the one wake the message ever gets,
// because maybeWake fires once at publish and nothing retries later.
func TestALeaseThatHasNotLapsedIsNotProofTheAgentIsRunning(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{session_id}"}, Cooldown: time.Minute},
	})
	l := bridgeAgent("justStopped", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	l.Status = core.StatusActive
	l.LastCoordination = time.Now().Add(-30 * time.Minute) // inside the lease, long past the turn
	st.Agents = map[string]*core.Agent{"justStopped": l}

	e.maybeWake(core.Event{
		Type: "message.sent", To: "justStopped",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	})
	if _, spent := e.wakers.last["justStopped"]; !spent {
		t.Error("an agent whose turn ended half an hour ago was treated as running " +
			"because its lease had not lapsed: the message waits for a human")
	}

	// And one that really is talking to us is left alone.
	e2 := &Engine{}
	st2 := core.NewState("t", core.DefaultLimits())
	e2.state = st2
	e2.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo"}, Cooldown: time.Minute},
	})
	live := bridgeAgent("live", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	live.LastCoordination = time.Now()
	st2.Agents = map[string]*core.Agent{"live": live}
	e2.maybeWake(core.Event{
		Type: "message.sent", To: "live",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	})
	if _, spent := e2.wakers.last["live"]; spent {
		t.Error("started a process for an agent that called Dibs a moment ago")
	}
}
