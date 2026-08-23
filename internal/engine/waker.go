package engine

import (
	"context"
	"errors"
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
	// deferred: a re-check armed for when an agent's cooldown expires, because
	// maybeWake fires once per event and nothing else retries.
	deferred map[string]*time.Timer
	// running: agents whose wake command has not exited yet.
	//
	// The cooldown alone was the whole exclusion, and it is a START-time rule:
	// ninety seconds by default against a command that may run for two hours,
	// so a later blocking event launched a second `codex exec resume` beside
	// the first and one thread got two activations interleaving into one
	// transcript. Which is the duplicate-process failure the cooldown exists to
	// prevent, arriving through the gap between "recently started" and "still
	// going".
	running map[string]bool
	// arrived: mail turned up for this agent while its wake command was still
	// running, so the exit has to look again.
	//
	// The running branch used to discard those events outright, on the reading
	// that the command IS this agent's activation and will read the mail. It
	// reads its inbox near the START of a turn that may last two hours, so
	// anything arriving after that read and before the exit was stranded until
	// some unrelated event happened to wake the agent again. Same defect as the
	// cooldown suppressing mail and forgetting it, one branch earlier.
	arrived map[string]bool
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
	// BEFORE THE RECENCY SHORT-CIRCUIT, because the wake IS what is in touch.
	//
	// The exit re-check was added so mail arriving after a running command has
	// read its inbox is not stranded for the rest of a two-hour turn. Reading
	// that inbox is a call to Dibs, so it updates e.seen and makes the agent
	// recently in touch, and the return below therefore fired before anything
	// recorded the arrival: the re-check never armed, on precisely the ordering
	// it exists for. The fix was correct and unreachable.
	//
	// Marked here, where a wake is known to be running for this agent, and only
	// for blocking news. Recently in touch with NO wake running is a different
	// agent altogether: one that is genuinely working and will see this at its
	// own turn boundary, which is why the short-circuit stays.
	if e.noteArrivalDuringWake(l.ID) {
		slog.Debug("mail arrived during a running wake; re-checking at its exit",
			"agent", l.ID)
		return
	}
	// Having called Dibs inside the cooldown is real evidence of a live agent,
	// and it is the same window that bounds the wake itself.
	if e.recentlyInTouch(l) {
		return
	}
	cmd, ok := e.wakeFor(l, msgType, ev)
	if !ok {
		return
	}
	agent, stamp := l.ID, e.wakeStamp(l.ID)
	e.wakers.mu.Lock()
	cool := e.wakers.byHarness[wakeHarness(l)].cooldown
	e.wakers.mu.Unlock()
	go func() {
		defer e.wakeExited(agent)
		if runWake(cmd, agent) {
			return
		}
		// A FAILED wake read nothing, so whatever arrived during it is still
		// owed. One re-check, armed here where failure is actually known:
		// retryWakeDecision does not arm another on ITS failure, so a command
		// that is simply wrong costs two attempts rather than looping.
		defer e.deferWakeLocked(agent, cool)
		// A FAILED WAKE MUST NOT SPEND THE ATTEMPT.
		//
		// The cooldown is taken before the process starts, which is right: two
		// messages arriving together must not become two processes. But a
		// command that fails to start, exits nonzero or times out woke nobody,
		// and holding the cooldown after it meant the ONE attempt this message
		// was ever going to get was consumed by a process that did nothing.
		// maybeWake fires per event and never retries, so `send` reported the
		// mailbox written while the recipient stayed stopped until some
		// unrelated event happened to arrive after the window: success with no
		// effect, on the one path this release exists to add.
		//
		// Released rather than retried here. Retrying in place would loop
		// against a command that is simply wrong, and the operator's log
		// already says so; letting the NEXT blocking message try again is the
		// behaviour an agent waiting for mail actually needs.
		e.releaseWake(agent, stamp)
	}()
}

// deferWake re-asks the wake question when this agent's cooldown expires.
//
// Callers hold wakers.mu.
//
// The timer is stored so a second suppressed event replaces it rather than
// adding one: three questions inside the window are one re-check, which is the
// same coalescing the cooldown was for. Stopping the old timer first is what
// makes that true; leaving it running would be the fork bomb with extra steps.
func (e *Engine) deferWake(agent string, in time.Duration) {
	if e.wakers.deferred == nil {
		e.wakers.deferred = map[string]*time.Timer{}
	}
	if t := e.wakers.deferred[agent]; t != nil {
		t.Stop()
	}
	// A small margin, so the timer does not land a microsecond early and find
	// the cooldown still nominally unexpired.
	e.wakers.deferred[agent] = time.AfterFunc(in+50*time.Millisecond, func() {
		e.retryWake(agent)
	})
}

// retryWake re-decides, on the writer loop, whether this agent still needs one.
//
// From scratch rather than from a remembered event: by now the agent may have
// come back on its own and read everything, another wake may be running, or the
// message may have been answered. The only thing worth carrying across the
// timer is the agent's name.
func (e *Engine) retryWake(agent string) {
	_, _ = e.query(context.Background(), func() core.Result {
		e.retryWakeDecision(agent)
		return core.Result{"ok": true}
	})
}

// deferWakeLocked is deferWake for callers that do not already hold the lock.
func (e *Engine) deferWakeLocked(agent string, in time.Duration) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	e.deferWake(agent, in)
}

// retryWakeDecision is the decision, split from the loop plumbing.
//
// Split because query() sends on e.ops, which is nil on an engine whose loop is
// not running: a test that called the wrapper would block forever rather than
// fail, which AGENTS.md records as having hung CI for five minutes once. It
// hung this one too, before the split.
//
// Callers run on the writer loop.
func (e *Engine) retryWakeDecision(agent string) {
	e.wakers.mu.Lock()
	// STOPPED, not merely forgotten. Deleting the map entry leaves the timer
	// running: a failed command arms a cooldown re-check, and if mail also
	// arrived during it the exit re-check runs immediately and dropped that
	// entry without stopping it. The orphan then fired after the cooldown and
	// started a THIRD command, against the adjacent promise that a bad command
	// costs two attempts rather than looping.
	if t := e.wakers.deferred[agent]; t != nil {
		t.Stop()
	}
	delete(e.wakers.deferred, agent)
	e.wakers.mu.Unlock()
	if e.state == nil {
		return
	}
	l := e.state.Agents[agent]
	if l == nil || l.Gone() {
		return
	}
	// The turn end is recorded by wakeExited, not here: the cooldown timer can
	// fire while a command is still running, and claiming a finished turn there
	// would be false. See noteWakeEnded.
	if e.recentlyInTouch(l) {
		return
	}
	if !e.hasBlockingMail(agent) {
		return
	}
	// A wake that is STILL running is already this agent's activation; the
	// mail will be read by it. Nothing owed, nothing to arm.
	e.wakers.mu.Lock()
	stillRunning := e.wakers.running[agent]
	e.wakers.mu.Unlock()
	if stillRunning {
		return
	}
	// THE OUTSTANDING WORK, NOT A PLACEHOLDER.
	//
	// This passed a hard-coded MsgQuestion and a bare event, so every wake that
	// went through a retry, which is now both the cooldown path and the exit
	// path, substituted `{type}` as "question" and `{from}` as empty, whatever
	// was actually waiting: a request, a handoff, an approval, a denial. Those
	// two placeholders are documented operator configuration, and a command
	// built on them was handed wrong arguments on precisely the paths this
	// release added. The verdict fix above solved the same problem for the
	// immediate case and left the retry.
	//
	// Derived from state rather than remembered from the event, which is right
	// for a re-check: by now the original message may have been answered and a
	// different one may be the reason this is still owed.
	kind, from := e.oldestBlocking(agent)
	cmd, ok := e.wakeFor(l, kind, core.Event{
		Type: "wake.retry", To: agent, Agent: from,
		Data: map[string]any{"msg_type": kind, "from": from},
	})
	if !ok {
		return
	}
	stamp := e.wakeStamp(agent)
	go func() {
		defer e.wakeExited(agent)
		if !runWake(cmd, agent) {
			// Released so the next EVENT may try, but no timer armed: this is
			// already the retry, and a command that fails twice fails.
			e.releaseWake(agent, stamp)
		}
	}()
}

// oldestBlocking names the work a re-check is being run for: the type and
// sender of the longest-waiting blocking message.
//
// "notice" when the reason is a blocking notice rather than mail, which is a
// real case (an approval, an eviction) and has no sender. Not one of the four
// message types, deliberately: a command reading `{type}` should be able to
// tell "somebody approved your request" from "somebody asked you a question",
// and calling it a question because that was the convenient constant is how
// this went wrong in the first place.
//
// Callers run on the writer loop.
func (e *Engine) oldestBlocking(agent string) (kind, from string) {
	var oldest *core.Message
	for _, m := range e.state.Inbox(agent) {
		blocking := m.Expecting() &&
			(m.State == core.MsgStatePending || m.State == core.MsgStateDelivered)
		blocking = blocking || (m.Type == core.MsgHandoff && m.State != core.MsgStateAcked)
		if !blocking {
			continue
		}
		if oldest == nil || m.Serial < oldest.Serial {
			oldest = m
		}
	}
	if oldest == nil {
		return "notice", ""
	}
	return oldest.Type, oldest.From
}

// hasBlockingMail reports whether anybody is still waiting on this agent.
//
// Callers run on the writer loop.
func (e *Engine) hasBlockingMail(agent string) bool {
	if e.blockingNotices(agent) > 0 {
		return true
	}
	for _, m := range e.state.Inbox(agent) {
		if m.Expecting() && (m.State == core.MsgStatePending || m.State == core.MsgStateDelivered) {
			return true
		}
		if m.Type == core.MsgHandoff && m.State != core.MsgStateAcked {
			return true
		}
	}
	return false
}

// noteArrivalDuringWake records blocking news that turned up while this agent's
// wake command was still running, so the exit looks again. It reports whether
// one was running, which is also the answer to "is there any point going on".
func (e *Engine) noteArrivalDuringWake(agent string) bool {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if !e.wakers.running[agent] {
		return false
	}
	if e.wakers.arrived == nil {
		e.wakers.arrived = map[string]bool{}
	}
	e.wakers.arrived[agent] = true
	return true
}

// wakeFinished records that this agent's wake command has exited, so a later
// blocking message may start another. It reports whether mail arrived while it
// was running, which is a question only the exit can answer.
func (e *Engine) wakeFinished(agent string) bool {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	delete(e.wakers.running, agent)
	if e.wakers.arrived[agent] {
		delete(e.wakers.arrived, agent)
		return true
	}
	return false
}

// wakeExited is wakeFinished plus the re-check, for the goroutine that ran the
// command.
//
// Split for the reason retryWake is split from retryWakeDecision: query() sends
// on e.ops, which is nil on an engine with no running loop, so a test calling
// this would block forever rather than fail. Tests take wakeFinished and its
// answer; production takes this.
func (e *Engine) wakeExited(agent string) {
	_, _ = e.query(context.Background(), func() core.Result {
		e.wakeExitedDecision(agent)
		return core.Result{"ok": true}
	})
}

// wakeExitedDecision is the exit, split from the loop plumbing so a test can
// call it: query() sends on e.ops, nil on an engine with no running loop, so a
// test calling the wrapper would block forever rather than fail.
//
// Callers run on the writer loop.
func (e *Engine) wakeExitedDecision(agent string) {
	// CLEARED AND RECORDED IN ONE TURN OF THE LOOP.
	//
	// wakeFinished ran out here, before the closure was queued, and the two
	// facts it produces are read by different branches of maybeWake. So an
	// ordinary message arriving in that window saw the agent as no longer
	// running (running was cleared) AND as recently in touch (the turn end
	// was not recorded yet): noteArrivalDuringWake declined to mark it,
	// recentlyInTouch declined to wake it, and nothing armed a deferred
	// re-check either. The sender was told it was delivered and the stopped
	// recipient was not reached.
	//
	// maybeWake runs on this loop too, so doing both inside one closure
	// makes the intermediate state unobservable rather than unlikely. A
	// window this narrow is not worth closing with a smaller window.
	owed := e.wakeFinished(agent)
	// ALWAYS, not only when a re-check is owed.
	//
	// The command runs the agent's whole turn in that process, so the
	// process exiting IS the turn finishing: that is true whether or not
	// mail happened to arrive while it ran, and it is what turnEnded means
	// on every path that has a Stop hook to say so. This one has none.
	//
	// Stamping it only on the owed path left the commonest ordering broken.
	// A wake runs, the agent reads its inbox (which is a call to Dibs, so it
	// is "recently in touch"), the command exits with nothing having
	// arrived, and THEN a question lands. maybeWake found no running wake,
	// found the agent recently in touch on the strength of a turn that had
	// already ended, and returned without even arming a deferred re-check.
	// The message was stored, reported delivered, and waited for a human.
	e.noteWakeEnded(agent)
	if owed {
		e.retryWakeDecision(agent)
	}
}

// noteWakeEnded records that a wake command has exited, which is the end of
// that agent's turn. Callers run on the writer loop.
func (e *Engine) noteWakeEnded(agent string) {
	if e.turnEnded == nil {
		e.turnEnded = map[string]time.Time{}
	}
	e.turnEnded[agent] = time.Now()
}

// wakeStamp reads back the cooldown this wake just took, so its failure can be
// matched to it later.
func (e *Engine) wakeStamp(agent string) time.Time {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	return e.wakers.last[agent]
}

// releaseWake forgets a cooldown whose wake never happened, so the next
// blocking message may try again.
//
// ONLY ITS OWN. A wake command may run for up to two hours, which is longer
// than any cooldown, so a later wake can start and take a new cooldown while an
// earlier one is still going. Deleting unconditionally let that earlier
// failure erase the NEWER attempt's cooldown, and the next event then started a
// third command beside the one already running: two resumptions of one thread,
// produced by the code that exists to stop exactly that.
//
// The timestamp is the generation. If it has moved, this failure is stale and
// has nothing to release.
func (e *Engine) releaseWake(agent string, mine time.Time) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if cur, ok := e.wakers.last[agent]; ok && cur.Equal(mine) {
		delete(e.wakers.last, agent)
	}
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
	if e.wakers.running[l.ID] {
		// STILL GOING is a stronger reason than recently started, and it
		// outlives the cooldown: the command IS the activation, so a second one
		// is a second agent in the same thread.
		//
		// RECORDED, AND RE-ASKED AT EXIT. Not on a timer.
		//
		// A timer here fires DURING the command and starts a second one beside
		// it, which is the coalescing this branch exists to provide and, on a
		// command that never reads mail, a retry loop with a ninety-second
		// fuse. Both of those were shipped and the wake e2e caught them.
		//
		// The exit is the right moment and has neither problem: any number of
		// messages during one run set one flag, nothing starts while the
		// command is alive, and the re-check asks hasBlockingMail, so an
		// activation that DID read its mail produces no second wake. What it
		// fixes is the case that reading early cannot cover, which is mail
		// arriving during the rest of a two-hour turn.
		if e.wakers.arrived == nil {
			e.wakers.arrived = map[string]bool{}
		}
		e.wakers.arrived[l.ID] = true
		slog.Debug("no wake: the last one is still running; re-checking at its exit",
			"agent", l.ID)
		return nil, false
	}
	if last, seen := e.wakers.last[l.ID]; seen && now.Sub(last) < cmd.cooldown {
		// SUPPRESSED, NOT DISCARDED.
		//
		// maybeWake fires once per event and nothing retries, so a message
		// arriving after a wake has EXITED but inside its cooldown was lost
		// outright: the recipient is asleep again, somebody is blocked on it,
		// and the next attempt waits for an unrelated event that may never
		// come. Ninety seconds is a rate limit on starting processes; it was
		// behaving as a rate limit on delivering mail, which is the failure
		// this whole path exists to remove.
		//
		// A timer for the remainder, re-deciding from scratch when it fires.
		// One per agent, replaced rather than stacked, so a burst inside the
		// window is still one wake at the end of it.
		e.deferWake(l.ID, cmd.cooldown-now.Sub(last))
		slog.Debug("no wake yet: inside the cooldown, re-checking when it expires",
			"agent", l.ID)
		return nil, false
	}
	e.wakers.last[l.ID] = now
	if e.wakers.running == nil {
		e.wakers.running = map[string]bool{}
	}
	e.wakers.running[l.ID] = true

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

// wakeGrace bounds the wait AFTER the deadline kills the command.
//
// cmd.WaitDelay, and it has to be set because stdout and stderr are not files.
// For a non-file writer os/exec copies through a pipe, and killing the process
// at the deadline does not close descriptors a GRANDCHILD inherited: Wait then
// blocks on EOF that never comes, forever, well past the two-hour bound this
// package advertises. `codex exec resume` starting a helper that outlives it is
// an ordinary thing for a wake command to do.
//
// The consequence was worse than a stuck goroutine. wakeFinished runs on defer,
// so it never ran, wakers.running kept that agent marked as still going, and
// every later message to it was refused as a duplicate: one leaked descriptor
// made an agent permanently unreachable until the daemon restarted. Two fixes
// of mine met, the tail buffer and the running map, and neither was wrong
// alone.
const wakeGrace = 10 * time.Second

// runWake executes one wake, bounded and out of the way, and reports whether
// anything was actually woken.
//
// The boolean is load-bearing: the caller releases the cooldown when this is
// false, so a command that could not run does not consume the single attempt
// the message was going to get.
func runWake(argv []string, agent string) bool {
	return runWakeFor(argv, agent, wakeTimeout, wakeGrace)
}

// runWakeFor is runWake with its bounds as arguments, so a test can assert that
// this RETURNS rather than assert that a constant is large. The old test
// checked only that wakeTimeout was at least two hours, which stays true while
// Wait blocks past it.
func runWakeFor(argv []string, agent string, timeout, grace time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// #nosec G204 -- argv comes from the operator's own config file and nowhere
	// else: SetWakeCommands is the only writer, no tool or op reaches it, and
	// substitution replaces whole elements rather than building a string. There
	// is no shell in this path.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// BOUNDED. CombinedOutput holds every byte until the process exits, and the
	// documented command is `codex exec resume`, which runs a whole agent turn
	// and may print a transcript for two hours. All of it sat in the daemon's
	// memory and was then thrown away on success. Only the tail is ever used:
	// it goes in the warning when the command fails, and a wake that failed says
	// why in its last few lines rather than its first thousand.
	tail := &tailBuffer{limit: 8 << 10}
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.WaitDelay = grace
	err := cmd.Run()
	out := tail.Bytes()
	if err != nil {
		// WHAT THE OS SAID, not what the agent printed.
		//
		// The documented wake command runs an entire agent turn, so its stdout
		// is transcript: decrypted mail, tool output, a model's summary of a
		// private message. Logging it put all of that on stderr and in
		// /api/logs, which undoes the reason mail is encrypted at rest.
		//
		// A command that never STARTED is different. There is no agent then,
		// and the bytes are the operating system's own complaint, which is the
		// half an operator actually needs to fix a wrong argv.
		fields := []any{"agent", agent, "cmd", argv[0], "err", err}
		var ee *exec.Error
		if errors.As(err, &ee) {
			fields = append(fields, "output", strings.TrimSpace(string(out)))
		}
		slog.Warn("wake command failed; the next message somebody is blocked on "+
			"will try again", fields...)
		return false
	}
	slog.Info("woke an agent that was not running", "agent", agent, "cmd", argv[0])
	return true
}

// tailBuffer keeps the last `limit` bytes written to it and discards the rest.
//
// A wake command is somebody else's program running for as long as an agent
// turn takes. Buffering all of it is an unbounded allocation controlled by
// whatever that program decides to print; keeping the tail is what a failure
// message actually needs.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) > t.limit {
		p = p[len(p)-t.limit:]
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return n, nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// wakeHarness is the table key for this agent, lowercased as SetWakeCommands
// stores it.
func wakeHarness(l *core.Agent) string {
	if l == nil || l.Agent == nil {
		return ""
	}
	return strings.ToLower(l.Agent.Harness)
}
