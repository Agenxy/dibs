package engine

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Reaching an agent that is not running.
//
// Every other delivery path in Dibs waits for the agent to come to it: a hook
// fires on the agent's own turn boundary, a call returns what is waiting, a
// long poll parks until something arrives. All of them need the agent to be
// executing already. An idle session has no boundary coming and makes no calls,
// so mail for it sat until a person went and said so out loud, which is what
// happened to this project's own operator twice in one day.
//
// A message service whose recipient must already be awake is a polling API with
// extra steps. So the board can now START something. What it starts is the
// operator's command, out of the operator's config file, and nothing an agent
// says reaches it.
//
// This is a deliberate reversal of the position WAKE-MECHANISMS.md argued for,
// and that document has been updated rather than left to contradict the code.
// The old argument was that a coordination service which drives harnesses
// becomes a wrapper for tools it does not own. That is a real cost, and it is
// smaller than the one it was avoiding: a board nobody can be reached on.

// wakeCooldown is the shortest gap between two wakes of one agent.
//
// A fleet that starts a process on every message is a fork bomb with better
// manners. The default is deliberately long: a wake exists to end a silence,
// not to shave seconds off a reply.
const wakeCooldown = 90 * time.Second

// wakeCommand is one harness's way in, already validated.
type wakeCommand struct {
	argv     []string
	cooldown time.Duration
}

// wakers holds the operator's wake commands and the last time each agent was
// woken. Guarded because the wake runs off the writer loop.
type wakers struct {
	mu        sync.Mutex
	byHarness map[string]wakeCommand
	last      map[string]time.Time
}

// SetWakeCommands installs the operator's wake table. Keyed by harness, as the
// agent self-reports it, lowercased.
//
// Called once at startup from the config. There is deliberately no tool, no
// admin route and no op that can reach this: a wake command is arbitrary code
// on the operator's machine, and the only party who may name it is the operator.
func (e *Engine) SetWakeCommands(cmds map[string]WakeCommand) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if e.wakers.byHarness == nil {
		e.wakers.byHarness = map[string]wakeCommand{}
	}
	for harness, c := range cmds {
		if len(c.Argv) == 0 {
			continue
		}
		cool := c.Cooldown
		if cool <= 0 {
			cool = wakeCooldown
		}
		e.wakers.byHarness[strings.ToLower(harness)] = wakeCommand{argv: c.Argv, cooldown: cool}
	}
}

// WakeCommand is one harness's entry, as the operator wrote it.
//
// Each carries its OWN cooldown. The first version took a single duration for
// the whole table and startup passed the largest one in it, so a cautious
// harness set to ten minutes silently throttled every other harness to ten
// minutes: settings that parsed, reported success, and did nothing they said.
type WakeCommand struct {
	Argv     []string
	Cooldown time.Duration
}

// wakeFields are the only substitutions a wake command gets.
//
// Each replaces a WHOLE argv element, never part of one, and the value is
// passed to exec as a single argument. There is no shell anywhere in this path,
// so a message body containing a semicolon is a message body containing a
// semicolon.
type wakeFields struct {
	// thread is the identifier the harness's own resume command accepts,
	// which is NOT the agent's session_id: that one names the harness
	// PROCESS ("host-92368") and no resume command has ever heard of it.
	// threadIDOf finds this; when it finds nothing, nothing is woken.
	thread  string
	agent   string
	from    string
	msgType string
	message string
}

func (f wakeFields) apply(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		switch a {
		case "{thread}":
			out = append(out, f.thread)
		case "{agent}":
			out = append(out, f.agent)
		case "{from}":
			out = append(out, f.from)
		case "{type}":
			out = append(out, f.msgType)
		case "{message}":
			out = append(out, f.message)
		default:
			out = append(out, a)
		}
	}
	return out
}

// maybeWake starts the operator's wake command for an agent that cannot be
// reached any other way. Called from publish, on the writer loop, and returns
// immediately: the command itself runs in its own goroutine.
func (e *Engine) maybeWake(ev core.Event) {
	// Mail AND verdicts. The first version took only message.sent, which
	// excluded every answer and approval: message.approved, .denied, .answered
	// and .declined are addressed to the agent that ASKED, and an agent that
	// asked and then stopped is the single clearest case for starting it again.
	// Leaving them out recreated, on the new mechanism, the exact defect the
	// notice work had just fixed on the old one.
	switch ev.Type {
	case "message.sent", "message.approved", "message.denied",
		"message.answered", "message.declined":
	default:
		return
	}
	if ev.To == "" {
		return
	}
	// Guarded, because this runs on the event path and must not be able to take
	// the daemon down over a wake. A zero-value Engine has no state at all,
	// which is how the notice tests are built, and a nil dereference here would
	// panic in the writer's own goroutine. Same trap as noteNewMember, found
	// the same way: by a test that builds the engine bare.
	if e.state == nil {
		return
	}
	l, ok := e.state.Agents[ev.To]
	if !ok {
		return
	}
	// RECENTLY IN TOUCH, not merely "active".
	//
	// Status was the first test here and it was wrong. `active` means the idle
	// lease has not lapsed, which is 45 minutes by default; Stop and SessionEnd
	// only finish the separate supervision child, so an agent whose turn ended
	// seconds ago is still `active` and is not running. Skipping on that
	// discarded the one wake attempt this message will ever get, because
	// maybeWake fires once when the event is published and the dormant sweep
	// never retries the mail. A message arriving just after a turn ended waited
	// for a human. Found by the pre-release review, which also pointed out my
	// test could not see it: nil engine state returned before this branch.
	//
	// Having called Dibs inside the cooldown is real evidence of a live agent,
	// and it is the same window that bounds the wake itself.
	if e.recentlyInTouch(l) {
		return
	}
	// Only news somebody is blocked on. An FYI does not justify starting a
	// process on the operator's machine.
	//
	// A VERDICT is always blocking and carries no msg_type: it is an answer to
	// something this agent asked and then stopped for. Filtering verdicts by a
	// field they do not have dropped every one of them, which is how the first
	// version excluded exactly the case with the strongest claim on a wake.
	msgType, _ := ev.Data["msg_type"].(string)
	if ev.Type == "message.sent" {
		switch msgType {
		case core.MsgQuestion, core.MsgRequest, core.MsgHandoff:
		default:
			return
		}
	}
	cmd, ok := e.wakeFor(l, msgType, ev)
	if !ok {
		return
	}
	go runWake(cmd, l.ID)
}

// recentlyInTouch reports whether this agent has spoken to the board lately
// enough that it is certainly running and will see the message on its own.
func (e *Engine) recentlyInTouch(l *core.Agent) bool {
	e.wakers.mu.Lock()
	harness := ""
	if l.Agent != nil {
		harness = strings.ToLower(l.Agent.Harness)
	}
	cmd, ok := e.wakers.byHarness[harness]
	e.wakers.mu.Unlock()
	if !ok {
		return false
	}
	return !l.LastCoordination.IsZero() &&
		time.Since(l.LastCoordination) < cmd.cooldown
}

// wakeFor picks the command for this agent and spends its cooldown.
//
// Split from maybeWake so the DECISION is testable without running anything: a
// test that has to spawn a process to find out whether it would have is a test
// nobody runs.
func (e *Engine) wakeFor(l *core.Agent, msgType string, ev core.Event) ([]string, bool) {
	harness := ""
	if l.Agent != nil {
		harness = l.Agent.Harness
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	cmd, ok := e.wakers.byHarness[strings.ToLower(harness)]
	if !ok || len(cmd.argv) == 0 {
		return nil, false
	}
	// THE THREAD ID, WHICH IS USUALLY NOT SessionID.
	//
	// This passed SessionID and that is wrong on the ordinary path. The stdio
	// bridge derives `host-<ppid>` and stores it as the primary id, while the
	// identifier `codex exec resume` accepts arrives separately as the harness's
	// own `_meta.threadId` and is kept as an ALIAS. So the shipped command ran
	// as `codex exec resume host-10602`, which resolves to no thread: the
	// subprocess started, failed, and the mail stayed unread. The feature did
	// not work in the one configuration anybody would use. Found by the
	// pre-release review; my test invented an agent whose primary id already
	// looked like a thread, so it could never have caught it.
	thread := threadIDOf(l)
	if thread == "" {
		slog.Debug("no wake: no harness thread id for this agent",
			"agent", l.ID, "session_id", l.SessionID)
		return nil, false
	}
	now := time.Now()
	if e.wakers.last == nil {
		e.wakers.last = map[string]time.Time{}
	}
	if last, seen := e.wakers.last[l.ID]; seen && now.Sub(last) < cmd.cooldown {
		slog.Debug("no wake: still inside the cooldown", "agent", l.ID)
		return nil, false
	}
	e.wakers.last[l.ID] = now

	from, _ := ev.Data["from"].(string)
	return wakeFields{
		thread:  thread,
		agent:   l.ID,
		from:    from,
		msgType: msgType,
		// Deliberately NOT the body. A wake says that mail exists; the agent
		// reads it over the authenticated channel with its own token. Putting
		// the text in an argv would hand a message's contents to whatever the
		// operator's command does with it, and mail is encrypted at rest for
		// exactly the opposite reason.
		// Phrased as an INSTRUCTION, never as a fact with a shelf life.
		//
		// "You have mail" can be false by the time it lands. A wake may be
		// queued durably and delivered minutes later, and another activation
		// may have read the mail in between; a resumed thread then wakes, finds
		// an empty inbox, and reasonably reports the wake as spurious. That
		// happened during this feature's own testing and cost a peer an
		// activation working out whether Dibs was lying to it.
		//
		// "Check" is true whenever it arrives.
		message: "Dibs: check the board. Call check_in, then inbox, and act on anything there.",
	}.apply(cmd.argv), true
}

// threadIDOf finds the identifier a harness's own resume command will accept.
//
// A harness thread id is a UUID; the bridge's synthetic `host-<ppid>` is not,
// and neither is a name a person typed. Shape is a weak discriminator in
// general and an exact one here, which is why it is used rather than guessed
// provenance: the alternative was a new replayable field, and a json tag added
// to core is a thing this repository has already lost data to.
//
// Returning "" means no wake. Starting a resume against an identifier the
// harness cannot resolve costs a process and delivers nothing, and starting one
// against a DIFFERENT valid thread would wake somebody else.
func threadIDOf(l *core.Agent) string {
	if looksLikeThreadID(l.SessionID) {
		return l.SessionID
	}
	for _, alias := range l.SessionAliases {
		if looksLikeThreadID(alias) {
			return alias
		}
	}
	return ""
}

// looksLikeThreadID reports whether s has the shape of a UUID: 8-4-4-4-12 hex
// with hyphens.
func looksLikeThreadID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// runWake executes one wake, bounded and out of the way.
func runWake(argv []string, agent string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- argv comes from the operator's own config file and nowhere
	// else: SetWakeCommands is the only writer, no tool or op reaches it, and
	// substitution replaces whole elements rather than building a string. There
	// is no shell in this path.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("wake command failed", "agent", agent, "cmd", argv[0],
			"err", err, "output", strings.TrimSpace(string(out)))
		return
	}
	slog.Info("woke an agent that was not running", "agent", agent, "cmd", argv[0])
}
