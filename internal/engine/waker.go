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
	// A RETIRED IDENTITY IS NOT WOKEN.
	//
	// Answering a closed or archived asker is allowed and returns
	// delivered:false, saying plainly that nobody will read it. The event is
	// still published, and every published event reaches this, so the board
	// said "nobody will read this answer" and then started the thread anyway.
	// Two costs: a subprocess spent on a mailbox that cannot be restored, and
	// worse, a closed PERSISTENT identity resuming into the nonce-registration
	// path and coming back ACTIVE, which is precisely the finality sign_off
	// promises.
	if l.Gone() {
		slog.Debug("no wake: this agent has signed off", "agent", l.ID)
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
	// THE EPHEMERAL TIMESTAMP, because the durable one is deliberately stale.
	//
	// Every authenticated read touches e.seen immediately and only checkpoints
	// LastCoordination once per AgentTTL/2, so a perfectly healthy agent's
	// durable timestamp is routinely minutes old. Reading only that one, this
	// declared a running agent asleep and started a second `codex exec resume`
	// against the thread it was already working in: the duplicate-process case
	// this check exists to prevent, produced by the check itself.
	//
	// Both, and the later wins: seen is authoritative when present, and
	// LastCoordination still covers an agent last heard from before this daemon
	// booted, whose seen entry does not exist.
	last := l.LastCoordination
	if s, ok := e.seen[l.ID]; ok && s.After(last) {
		last = s
	}
	// A STOP SINCE THEN ENDS IT. Recency is a stand-in for "is it running", and
	// the harness answers that question directly when a turn finishes. Without
	// this, an agent that called in and then stopped two seconds later read as
	// running for the rest of the cooldown, and the one wake its next blocking
	// message was ever going to get was skipped.
	if done, ok := e.turnEnded[l.ID]; ok && done.After(last) {
		return false
	}
	return !last.IsZero() && time.Since(last) < cmd.cooldown
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

	// A VERDICT PUTS THESE SOMEWHERE ELSE.
	//
	// `message.sent` carries from and msg_type in Data. A verdict carries
	// neither: the responder is Event.Agent and the disposition is the event
	// TYPE itself. Reading Data alone left `{from}` and `{type}` substituted
	// with empty strings on every approval, denial, answer and decline, so two
	// documented placeholders silently produced nothing in the one case with
	// the strongest claim on a wake. Whichever field the event actually used.
	from, _ := ev.Data["from"].(string)
	if from == "" {
		from = ev.Agent
	}
	kind := msgType
	if kind == "" {
		kind = strings.TrimPrefix(ev.Type, "message.")
	}
	return wakeFields{
		thread:  thread,
		agent:   l.ID,
		from:    from,
		msgType: kind,
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
//
// THE NEWEST ONE, and the order matters. bindHarnessSession APPENDS, so a
// persistent agent that has reattached three times holds three uuids with the
// current activation last. Taking the first ran `codex exec resume` against a
// thread the agent rotated away from days ago: a real thread, so the command
// succeeded, and the wrong one, so the agent that was waiting stayed asleep
// while a stale activation woke up holding somebody else's mail prompt. Every
// wake test built exactly one alias, which is the one arrangement that cannot
// tell the two orders apart.
func threadIDOf(l *core.Agent) string {
	for i := len(l.SessionAliases) - 1; i >= 0; i-- {
		if looksLikeThreadID(l.SessionAliases[i]) {
			return l.SessionAliases[i]
		}
	}
	// Only if no alias is a thread: the primary is the OLDEST name this agent
	// has had whenever aliases exist at all, because reattachment appends.
	if looksLikeThreadID(l.SessionID) {
		return l.SessionID
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

// wakeTimeout is the longest a wake command may run before it is killed.
//
// This was 30 seconds, which is a sensible bound for a notification and a
// catastrophic one for the command actually documented: `codex exec resume`
// continues the thread IN THIS PROCESS, so the timeout is a cap on the agent's
// whole turn. An ordinary turn passes 30s easily, and the kill landed mid-work
// with the cooldown already spent and no retry, so the wake destroyed the
// activation it had just created and the blocking message stayed unread. The
// bound exists only to stop a wedged process living forever; it must be far
// past any turn a person would wait for, and this is.
const wakeTimeout = 2 * time.Hour

// runWake executes one wake, bounded and out of the way.
func runWake(argv []string, agent string) {
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
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
