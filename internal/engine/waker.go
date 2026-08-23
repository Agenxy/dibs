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
func (e *Engine) SetWakeCommands(cmds map[string][]string, cooldown time.Duration) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if e.wakers.byHarness == nil {
		e.wakers.byHarness = map[string]wakeCommand{}
	}
	if cooldown <= 0 {
		cooldown = wakeCooldown
	}
	for harness, argv := range cmds {
		if len(argv) == 0 {
			continue
		}
		e.wakers.byHarness[strings.ToLower(harness)] = wakeCommand{argv: argv, cooldown: cooldown}
	}
}

// wakeFields are the only substitutions a wake command gets.
//
// Each replaces a WHOLE argv element, never part of one, and the value is
// passed to exec as a single argument. There is no shell anywhere in this path,
// so a message body containing a semicolon is a message body containing a
// semicolon.
type wakeFields struct {
	sessionID string
	agent     string
	from      string
	msgType   string
	message   string
}

func (f wakeFields) apply(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		switch a {
		case "{session_id}":
			out = append(out, f.sessionID)
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
	if ev.Type != "message.sent" || ev.To == "" {
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
	// An agent that is ACTIVE will see this the ordinary way: its next call, or
	// its own turn boundary. Starting something for it would be paying for a
	// wake that was already going to happen.
	if l.Status == core.StatusActive {
		return
	}
	// Only mail somebody is blocked on. An FYI does not justify starting a
	// process on the operator's machine, and the classification already exists.
	msgType, _ := ev.Data["msg_type"].(string)
	switch msgType {
	case core.MsgQuestion, core.MsgRequest, core.MsgHandoff:
	default:
		return
	}
	cmd, ok := e.wakeFor(l, msgType, ev)
	if !ok {
		return
	}
	go runWake(cmd, l.ID)
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
	// No session id, nowhere to send it. Every wake mechanism we know of
	// addresses a thread, and guessing one would wake somebody else.
	if l.SessionID == "" {
		slog.Debug("no wake: the agent has no session id", "agent", l.ID)
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
		sessionID: l.SessionID,
		agent:     l.ID,
		from:      from,
		msgType:   msgType,
		// Deliberately NOT the body. A wake says that mail exists; the agent
		// reads it over the authenticated channel with its own token. Putting
		// the text in an argv would hand a message's contents to whatever the
		// operator's command does with it, and mail is encrypted at rest for
		// exactly the opposite reason.
		message: "Dibs: you have mail. Call check_in, then inbox, and act on what is there.",
	}.apply(cmd.argv), true
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
