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
		"codex": {Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"}, Cooldown: time.Minute},
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
	// THE AGENT-DERIVED PLACEHOLDERS ARE IN THE COMMAND.
	//
	// This used a command containing only {thread} and {message}, so the hostile
	// agent id and sender it then set never entered argv at all and the loop
	// below had nothing to inspect. It passed whatever the substitution did,
	// which is the behaviour it exists to constrain. {agent}, {from} and {type}
	// are the three an agent can actually influence, so they are the three that
	// have to be here.
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{
			"codex", "queue", "--thread", "{thread}", "--message", "{message}",
			"--for", "{agent}", "--from", "{from}", "--kind", "{type}",
		}, Cooldown: time.Minute},
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
	// The hostile string may appear ONLY where a placeholder invited it, and
	// only as one whole element: exec takes it as a single argument, and there
	// is no shell in this path to reparse it.
	invited := map[int]bool{}
	for i, a := range []string{
		"codex", "queue", "--thread", "{thread}", "--message", "{message}",
		"--for", "{agent}", "--from", "{from}", "--kind", "{type}",
	} {
		invited[i] = strings.HasPrefix(a, "{")
	}
	seen := 0
	for i, a := range argv {
		if strings.Contains(a, nasty) {
			if !invited[i] {
				t.Errorf("argv[%d] = %q took hostile input at a position the operator "+
					"wrote literally: %v", i, a, argv)
			}
			if a != nasty {
				t.Errorf("argv[%d] = %q: a substituted value must replace a WHOLE "+
					"element. Pasting it into a larger string is where quoting bugs "+
					"live, and there is no shell here to blame for them", i, a)
			}
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("the hostile agent id reached no argv element at all, so this test " +
			"inspected nothing. The command must contain the agent-derived " +
			"placeholders for the check below to mean anything")
	}
	if len(argv) != 12 {
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

	// THE AGENT HAS TO BE ON THE BOARD.
	//
	// This built an agent locally and never put it in the engine's state, so
	// maybeWake returned at the nil-state guard before it looked anything up.
	// The assertion then confirmed that an untouched map was empty, which it
	// would have been with the recency check deleted outright: a vacuous guard
	// standing exactly where the real one is claimed to be.
	t.Run("an agent that has just called in is already reachable", func(t *testing.T) {
		en := &Engine{}
		st := core.NewState("t", core.DefaultLimits())
		en.state = st
		en.seen = map[string]time.Time{}
		en.SetWakeCommands(map[string]WakeCommand{
			"codex": {Argv: []string{"codex", "queue"}, Cooldown: time.Minute},
		})
		l := bridgeAgent("live", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
		l.Status = core.StatusActive
		l.LastCoordination = time.Now()
		st.Agents = map[string]*core.Agent{"live": l}

		en.maybeWake(core.Event{
			Type: "message.sent", To: "live",
			Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
		})
		if _, seen := en.wakers.last["live"]; seen {
			t.Error("an agent that called the board a moment ago was woken: it will " +
				"see this on its own next call, and starting a second activation " +
				"for it is paying twice and interleaving two processes in one thread")
		}
		// And a stopped one in the same engine IS woken, so this cannot pass by
		// waking nobody at all, which is how it passed before.
		gone := bridgeAgent("gone", "Codex", "019fff00-1111-7f60-81cc-6ab1298d76ec")
		gone.LastCoordination = time.Now().Add(-time.Hour)
		st.Agents["gone"] = gone
		en.maybeWake(core.Event{
			Type: "message.sent", To: "gone",
			Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
		})
		if _, seen := en.wakers.last["gone"]; !seen {
			t.Fatal("no agent in this engine can be woken at all, so the check above " +
				"proves nothing about recency")
		}
	})

	// An FYI must not start a process on the operator's machine.
	//
	// This asked the type-BLIND function and then logged whichever answer it
	// got, so both branches passed: deleting the production MsgNotify guard
	// left it green. It was a decoration in the shape of a regression test, and
	// the rule it names is the one that decides whether an operator leaves this
	// feature switched on.
	//
	// maybeWake is the filter, so maybeWake is what gets asked.
	t.Run("an FYI does not justify starting a process", func(t *testing.T) {
		l := bridgeAgent("sleeper2", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
		e.state = &core.State{Agents: map[string]*core.Agent{"sleeper2": l}}
		e.maybeWake(core.Event{
			Type: "message.sent", To: "sleeper2",
			Data: map[string]any{"msg_type": core.MsgNotify, "from": "someone"},
		})
		if _, woken := e.wakers.last["sleeper2"]; woken {
			t.Error("a notify started the operator's wake command. Nobody is " +
				"blocked on an FYI: it arrives at the agent's next activation " +
				"and costs nothing, and spawning a process for one is what " +
				"makes an operator turn this off")
		}
		// And the same agent, one message later, IS woken: otherwise this
		// passes for an agent that could never be woken at all, which is how a
		// filter test quietly stops testing the filter.
		e.maybeWake(core.Event{
			Type: "message.sent", To: "sleeper2",
			Data: map[string]any{"msg_type": core.MsgQuestion, "from": "someone"},
		})
		if _, woken := e.wakers.last["sleeper2"]; !woken {
			t.Fatal("a question did not wake the same agent either, so the check " +
				"above proved nothing about the TYPE filter")
		}
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
		"codex": {Argv: []string{"codex", "exec", "resume", "{thread}"}, Cooldown: time.Minute},
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
				"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: time.Hour},
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
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute},
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

// A persistent agent that has reattached is woken on the thread it is IN.
//
// bindHarnessSession appends, so the aliases of an agent that has come back
// three times read oldest-first with the current activation last. threadIDOf
// returned the first, so a wake resumed a thread the agent rotated away from
// days ago: a real uuid, so the command succeeded and the daemon logged a wake,
// and the wrong one, so the agent that was waiting stayed asleep. Reattaching
// is the ordinary life of a persistent agent and not an edge case; this session
// did it twice.
//
// Every other wake test builds exactly one alias, which is the single
// arrangement in which both orders give the same answer.
func TestTheWakeResumesTheAgentsCurrentThreadAndNotAnOldOne(t *testing.T) {
	const (
		yesterday = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		earlier   = "019fff00-1111-7f60-81cc-6ab1298d76ec"
		now       = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	e := &Engine{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"codex", "exec", "resume", "{thread}"}, Cooldown: time.Minute},
	})

	l := bridgeAgent("returning", "Codex", "")
	// The order bindHarnessSession produces: appended, oldest first.
	l.SessionAliases = []string{yesterday, earlier, now}

	argv, ok := e.wakeFor(l, core.MsgQuestion, core.Event{})
	if !ok {
		t.Fatal("no wake for a dormant agent with three known threads")
	}
	got := argv[len(argv)-1]
	if got == yesterday || got == earlier {
		t.Fatalf("the wake resumes %s, which this agent left. Aliases are appended, "+
			"so the CURRENT activation is the last one (%s): resuming an older thread "+
			"starts a real session that is not the one holding the mail, and the "+
			"daemon logs a successful wake for an agent that never hears anything",
			got, now)
	}
	if got != now {
		t.Fatalf("the wake resumes %q, which is neither the current thread nor a "+
			"known older one", got)
	}
}

// An agent that just made an authenticated call is running, whatever the
// durable timestamp says.
//
// Two clocks track the same fact and only one is current. Every authenticated
// read stamps e.seen at once; LastCoordination is a durable checkpoint written
// at most once per AgentTTL/2, so a perfectly healthy agent's durable
// timestamp is routinely minutes old. recentlyInTouch consulted only that one,
// so a running agent read as asleep and Dibs started a second `codex exec
// resume` in the thread it was already working in: two activations of one agent
// interleaving into one transcript. That is the duplicate-process failure the
// check exists to prevent, manufactured by the check.
//
// The existing lease test sets LastCoordination directly and never populates
// e.seen, so it exercises the fallback and not the production path.
func TestAnAgentThatJustCalledInIsNotWokenOnTopOfItself(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.seen = map[string]time.Time{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
	})

	l := bridgeAgent("working", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	// The shape the production path produces: the durable checkpoint is stale
	// by design, and the ephemeral one says the agent called in seconds ago.
	l.LastCoordination = time.Now().Add(-2 * time.Minute)
	e.seen["working"] = time.Now().Add(-2 * time.Second)
	st.Agents = map[string]*core.Agent{"working": l}

	e.maybeWake(core.Event{
		Type: "message.sent", To: "working",
		Data: map[string]any{"msg_type": core.MsgRequest, "from": "asker"},
	})
	if _, spent := e.wakers.last["working"]; spent {
		t.Error("started a wake for an agent that called in two seconds ago. " +
			"LastCoordination is a coalesced checkpoint and is stale on every " +
			"healthy agent; e.seen is the authoritative one. Resuming a thread " +
			"that is mid-turn gives one agent two activations")
	}

	// And the fallback still works: an agent with no e.seen entry, last heard
	// from before this daemon booted, is asleep and must be woken.
	old := bridgeAgent("beforeBoot", "Codex", "019fff00-1111-7f60-81cc-6ab1298d76ec")
	old.LastCoordination = time.Now().Add(-2 * time.Hour)
	st.Agents["beforeBoot"] = old
	e.maybeWake(core.Event{
		Type: "message.sent", To: "beforeBoot",
		Data: map[string]any{"msg_type": core.MsgRequest, "from": "asker"},
	})
	if _, spent := e.wakers.last["beforeBoot"]; !spent {
		t.Error("an agent with no e.seen entry and a two-hour-old checkpoint was " +
			"treated as running: preferring e.seen must not mean ignoring the " +
			"durable timestamp when there is nothing else")
	}
}

// A wake command may take as long as an agent's turn takes.
//
// The bound was 30 seconds, which is right for a notification and wrong for the
// command this project actually documents. `codex exec resume` continues the
// thread IN THAT PROCESS: it registers, reads its mail and does the work, so
// the timeout is a cap on the agent's entire turn, not on starting one. An
// ordinary turn passes 30s without trying, and the kill landed mid-work with
// the cooldown already spent and no retry for the blocking message, so the wake
// destroyed the activation it had just created and left the mail unread. That
// is worse than not waking: it burns a turn and reports success in the log.
//
// The bound stays, because a wedged process should not live forever. It has to
// sit far past any turn a person would wait for.
func TestAWakeCommandIsNotKilledMidTurn(t *testing.T) {
	// The longest a message can ask anybody to wait: SPEC's request deadline
	// cap. A wake that dies before the deadline it was sent to satisfy cannot
	// deliver the answer, whatever else it does.
	const longestAnyoneWaits = 2 * time.Hour
	if wakeTimeout < longestAnyoneWaits {
		t.Errorf("wakeTimeout is %s, and a request may carry a deadline of %s. The "+
			"documented command runs the agent's whole turn inside this bound, so "+
			"anything shorter kills real work: the cooldown is spent, maybeWake is "+
			"not called again for that event, and the message stays unread",
			wakeTimeout, longestAnyoneWaits)
	}
}

// A turn that ENDED must not go on looking like a running agent.
//
// The waker treats recent contact as evidence the agent is live, which it has
// to: an idle lease says nothing, since it lapses in 45 minutes. But recency
// alone is wrong in the ordinary case, and the ordinary case is a turn that
// ends seconds after its last call. For the remainder of the cooldown that
// agent read as running, so the next blocking message got no wake, and
// maybeWake fires once per event and never retries: the message waited for a
// human. A longer configured cooldown makes the hole bigger, not safer.
//
// This is the production sequence and nothing else: call in, Stop, then mail.
// The two existing tests use a 30-minute-old stopped agent and a two-second-old
// live one, and neither drives a stop after recent contact.
func TestATurnThatEndedIsNotMistakenForARunningAgent(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.seen = map[string]time.Time{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
	})

	l := bridgeAgent("stopped", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	l.SessionID = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st.Agents = map[string]*core.Agent{"stopped": l}

	// It called the board five seconds ago: well inside the cooldown.
	e.seen["stopped"] = time.Now().Add(-5 * time.Second)
	// And then its turn ended, which is what the harness tells us on Stop.
	e.noteTurnState(l, "Stop")

	// Blocking mail arrives while the old rule still called it "in touch".
	e.maybeWake(core.Event{
		Type: "message.sent", To: "stopped",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if _, spent := e.wakers.last["stopped"]; !spent {
		t.Error("no wake for an agent whose turn ended after its last call. " +
			"Recent contact is a stand-in for \"is it running\", and Stop answers " +
			"that question outright: treating the cooldown as proof of life " +
			"discards the single wake this message will ever get, and nothing " +
			"retries it")
	}

	// A NEW TURN RETRACTS THE STOP, before the model has called anything.
	//
	// The first version of this fix recorded only the stop, so one true
	// statement became a permanent one: SessionStart resolved to the agent, the
	// stale verdict still won, and blocking mail arriving before the model's
	// first authenticated call resumed a thread that was already running. That
	// is the duplicate activation the recency guard exists to prevent,
	// reintroduced by the fix for its opposite. The case below models an
	// authenticated call after the stop, which is a LATER point in the same
	// sequence and cannot see this.
	for _, start := range []string{"SessionStart", "UserPromptSubmit"} {
		t.Run("a new turn began with "+start, func(t *testing.T) {
			en := &Engine{}
			st := core.NewState("t", core.DefaultLimits())
			en.state = st
			en.seen = map[string]time.Time{}
			en.SetWakeCommands(map[string]WakeCommand{
				"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
			})
			a := bridgeAgent("restarted", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
			st.Agents = map[string]*core.Agent{"restarted": a}

			en.seen["restarted"] = time.Now().Add(-5 * time.Second)
			en.noteTurnState(a, "Stop") // the turn ended
			en.noteTurnState(a, start)  // and a new one began
			en.maybeWake(core.Event{
				Type: "message.sent", To: "restarted",
				Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
			})
			if _, spent := en.wakers.last["restarted"]; spent {
				t.Errorf("%s did not retract the earlier Stop, so the board resumed a "+
					"thread that is running right now. The model has not called Dibs "+
					"in this turn yet, and it does not have to: the harness already "+
					"said the session started", start)
			}
		})
	}

	// And contact AFTER the stop means it is running again, so it is left alone:
	// otherwise this passes by waking an agent that can never be quiet.
	e2 := &Engine{}
	st2 := core.NewState("t", core.DefaultLimits())
	e2.state = st2
	e2.seen = map[string]time.Time{}
	e2.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
	})
	back := bridgeAgent("resumed", "Codex", "019fff00-1111-7f60-81cc-6ab1298d76ec")
	st2.Agents = map[string]*core.Agent{"resumed": back}
	e2.noteTurnState(back, "Stop")
	e2.seen["resumed"] = time.Now() // a new turn, after the stop
	e2.maybeWake(core.Event{
		Type: "message.sent", To: "resumed",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if _, spent := e2.wakers.last["resumed"]; spent {
		t.Error("woke an agent that called in after its last stop: a finished turn " +
			"is not a permanent verdict, and starting a second activation on top " +
			"of a live one is what the recency check exists to prevent")
	}
}

// wakeEngine builds an engine the waker can actually run against.
//
// EXISTS BECAUSE THE ZERO VALUE IS A TRAP. maybeWake returns at `e.state == nil`
// before it looks up anything, so a test that builds `&Engine{}`, constructs an
// agent locally and calls maybeWake asserts nothing at all: the wakers map it
// then inspects was never going to be written to. Four tests in this file were
// written that way and every one of them passed against the bug it named, one
// of them for the recency check it was the only guard for.
//
// The fix is not to remember; it is to have a way of building one that works.
// Every wake test that goes through maybeWake uses this.
func wakeEngine(t *testing.T, cmd WakeCommand) (*Engine, *core.State) {
	t.Helper()
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	st.Agents = map[string]*core.Agent{}
	e.state = st
	e.seen = map[string]time.Time{}
	e.turnEnded = map[string]time.Time{}
	e.SetWakeCommands(map[string]WakeCommand{"codex": cmd})
	return e, st
}

// The trap itself: a wake test that forgets the state proves nothing, and it
// proves nothing SILENTLY, which is why it happened four times.
//
// This is not testing the nil guard for its own sake. It pins the reason
// wakeEngine exists, so that a later reader who finds the helper redundant and
// inlines `&Engine{}` again is told what that costs.
func TestMaybeWakeOnAnEngineWithNoStateDoesNothingAtAll(t *testing.T) {
	bare := &Engine{}
	bare.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute},
	})
	// A dormant agent with a resumable thread: everything a wake needs, except
	// that the engine has never heard of it.
	bare.maybeWake(core.Event{
		Type: "message.sent", To: "sleeper",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if len(bare.wakers.last) != 0 {
		t.Fatal("this engine woke something, so the premise below is wrong and " +
			"wakeEngine's comment needs rewriting")
	}

	// The same call against a wired engine DOES wake it. That difference is the
	// whole hazard: both look like a passing test, and only one is one.
	e, st := wakeEngine(t, WakeCommand{Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute})
	st.Agents["sleeper"] = bridgeAgent("sleeper", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	e.maybeWake(core.Event{
		Type: "message.sent", To: "sleeper",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if _, spent := e.wakers.last["sleeper"]; !spent {
		t.Error("wakeEngine does not produce an engine that can wake anybody, so " +
			"every test built on it is as vacuous as the ones it replaced")
	}
}

// An agent that has signed off is not started again.
//
// Answering a closed or archived asker is allowed: the response records
// delivered:false and says outright that nobody will read it. The event is
// published all the same, and every published event reaches maybeWake, so the
// board told the responder "nobody will read this" and then launched the
// operator's resume command against the retired thread. The wasted subprocess
// is the small half. The large half is that a closed PERSISTENT identity
// resuming goes through the nonce-registration path and comes back ACTIVE,
// which is exactly the finality sign_off promises.
func TestAnAgentThatSignedOffIsNotResumed(t *testing.T) {
	for _, status := range []core.AgentStatus{core.StatusClosed, core.StatusArchived} {
		t.Run(string(status), func(t *testing.T) {
			e, st := wakeEngine(t, WakeCommand{
				Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute,
			})
			l := bridgeAgent("retired", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
			l.Status = status
			st.Agents["retired"] = l

			// A verdict on something it asked before it went: the case that
			// actually happens, and the one with the strongest claim on a wake
			// if the agent were still here.
			e.maybeWake(core.Event{
				Type: "message.approved", Agent: "lael", To: "retired",
				Data: map[string]any{"msg_serial": uint64(7)},
			})
			if _, spent := e.wakers.last["retired"]; spent {
				t.Errorf("the board resumed a %s agent. The answer it is being woken "+
					"for was recorded as delivered:false because nobody will read it, "+
					"and resuming a closed persistent identity walks it back to active: "+
					"sign_off is supposed to be final", status)
			}
		})
	}

	// And a live agent in the same shape IS woken, so this cannot pass by
	// waking nobody.
	e, st := wakeEngine(t, WakeCommand{Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute})
	st.Agents["here"] = bridgeAgent("here", "Codex", "019fff00-1111-7f60-81cc-6ab1298d76ec")
	e.maybeWake(core.Event{
		Type: "message.approved", Agent: "lael", To: "here",
		Data: map[string]any{"msg_serial": uint64(7)},
	})
	if _, spent := e.wakers.last["here"]; !spent {
		t.Fatal("no agent is woken by an approval at all, so the checks above say " +
			"nothing about having signed off")
	}
}

// A verdict fills {from} and {type}, which it did not.
//
// `message.sent` carries from and msg_type inside Data; a verdict carries
// neither, because the responder is Event.Agent and the disposition is the
// event type itself. Reading Data alone substituted empty strings into two
// documented placeholders, on exactly the events an agent stopped and waited
// for. An operator's command would receive `--from "" --kind ""` and could not
// say who had answered.
func TestAVerdictWakeCarriesWhoAnsweredAndWhatTheyDid(t *testing.T) {
	for _, c := range []struct{ event, wantType string }{
		{"message.approved", "approved"},
		{"message.denied", "denied"},
		{"message.answered", "answered"},
		{"message.declined", "declined"},
	} {
		t.Run(c.event, func(t *testing.T) {
			e, st := wakeEngine(t, WakeCommand{
				Argv:     []string{"codex", "resume", "{thread}", "--from", "{from}", "--kind", "{type}"},
				Cooldown: time.Minute,
			})
			l := bridgeAgent("asker", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
			st.Agents["asker"] = l

			ev := core.Event{
				Type: c.event, Agent: "lael", To: "asker",
				Data: map[string]any{"msg_serial": uint64(7)},
			}
			argv, ok := e.wakeFor(l, "", ev)
			if !ok {
				t.Fatal("no wake for a verdict, so this proves nothing about its fields")
			}
			from, kind := argv[4], argv[6]
			if from != "lael" {
				t.Errorf("{from} = %q, want \"lael\": the responder is Event.Agent on a "+
					"verdict, and reading only Data hands the operator's command an "+
					"empty string where the answerer's name belongs", from)
			}
			if kind != c.wantType {
				t.Errorf("{type} = %q, want %q: a verdict's disposition is the event "+
					"type, and it is the thing the woken agent most needs to know",
					kind, c.wantType)
			}
		})
	}
}
