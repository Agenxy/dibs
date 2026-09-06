package engine

import (
	"context"
	"errors"
	"log/slog"
	"os"
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
	fallback []string
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
	// attempts counts executions per agent for the mail currently owed, so
	// a failure is retried once whichever path ran the first attempt. The
	// retry path treated every execution as the already-retried one, and a
	// first attempt that arrived there deferred (recency, boot) failed with no
	// timer left. Cleared on success and when nothing is owed. Found by the
	// pre-release review, round twenty-three.
	attempts map[string]int
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
		e.wakers.byHarness[strings.ToLower(harness)] = wakeCommand{
			argv: c.Argv, fallback: c.Fallback, cooldown: cool,
		}
	}
}

// WakeCommand is one harness's entry, as the operator wrote it.
//
// Each carries its OWN cooldown. The first version took a single duration for
// the whole table and startup passed the largest one in it, so a cautious
// harness set to ten minutes silently throttled every other harness to ten
// minutes: settings that parsed, reported success, and did nothing they said.
type WakeCommand struct {
	Argv []string
	// Fallback runs only when Argv exits non-zero. See boardconfig.WakeExec
	// for why a harness can need two: codex resumes a closed thread with one
	// command and reaches an open one with another, and each fails or parks
	// silently on the other's case.
	Fallback []string
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
	// Only news somebody is blocked on. The rule is core.WakeWorthy, shared
	// with the bridge's self-wake so both routes wake for the same mail.
	msgType, _ := ev.Data["msg_type"].(string)
	if !core.WakeWorthy(ev.Type, msgType) {
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
	//
	// BUT COME BACK, rather than spending the only attempt this message gets.
	//
	// A bare return here was the whole delivery for a message that arrived
	// inside the window. The justification was that the agent "is genuinely
	// working and will see this at its own turn boundary", and that is true only
	// where a turn boundary REACHES Dibs. An agent whose harness sends no
	// lifecycle hooks has none: nothing marks its turn ended, so recency simply
	// decays into silence and the message is never delivered by anything.
	//
	// Measured on this board. A question was sent to an active codex agent 40
	// seconds after its last call, well inside the 90-second window. No wake was
	// attempted, none was ever attempted afterwards, and the daemon's log shows
	// that harness has never delivered a lifecycle hook at all: it runs under
	// the desktop app, which does not read the CLI's hooks file. Every Codex
	// desktop agent on that board is in the same position.
	//
	// So the window becomes a deferral instead of a verdict. If the agent really
	// is working it will call again, the re-check will find it recently in touch
	// and defer once more; when it stops, the wake fires. hasBlockingMail bounds
	// the loop: it ends the moment the mail is read, answered or expires.
	if e.recentlyInTouch(l) {
		e.deferWakeLocked(l.ID, e.recencyWindow(l))
		slog.Debug("no wake yet: called Dibs recently, so re-checking when that "+
			"window closes", "agent", l.ID)
		return
	}
	cmd, ok := e.wakeFor(l, msgType, ev)
	if !ok {
		// A SNAPSHOT MISS IS NOT A VERDICT. The socket route decides from a
		// cache the loop never refreshes, so a session started after the last
		// scan was invisible to the one wake attempt its mail would ever get:
		// the refusal was final and the thirty-second refresh revisits no mail.
		// The retry below refreshes the cache before it decides. Found by the
		// pre-release review, round seven.
		if e.socketMayHaveAppeared(l) {
			e.deferWakeLocked(l.ID, peerCacheTTL)
		}
		return
	}
	agent, stamp := l.ID, e.wakeStamp(l.ID)
	// THE PLAN'S COOLDOWN, not another lookup. See wakePlan.cooldown.
	cool := cmd.cooldown
	go func() {
		defer e.wakeExited(agent)
		e.noteWakeAttempt(agent)
		if e.runWake(cmd, agent) {
			e.clearWakeAttempts(agent)
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
		// Off the loop, before the decision: the socket route reads the cache
		// without refreshing it, and a retry that decided from the same stale
		// snapshot would refuse for the same wrong reason.
		_ = e.peerSessions()
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
// peerRecheckEvery is how often outstanding blocking mail is reconsidered for
// an agent whose harness speaks the socket while no socket for it is found:
// the cache's own refresh cadence.
const peerRecheckEvery = 30 * time.Second

// bootRetryDelay is how long after boot the outstanding-mail retries run:
// long enough for the loop to be serving, since a retry is posted to it.
var bootRetryDelay = time.Second

// rearmDeferredWakes arms one retry for every agent holding blocking mail at
// boot, and reports how many. A deferred wake is a timer, and a restart lost
// it: a question sent inside the recipient's recency window, then a restart
// before the timer fired, left mail in the ledger that nothing would ever
// wake anybody for, since boot rebuilt notices and primed the socket cache and
// the sweeps retry no delivery. The retry makes the decision a fresh arrival
// would, cooldowns and recency included. Found by the pre-release review,
// round eight.
func (e *Engine) rearmDeferredWakes() int {
	if e.state == nil {
		return 0
	}
	n := 0
	for id, l := range e.state.Agents {
		if l.Gone() || !e.hasBlockingMail(id) {
			continue
		}
		e.deferWakeLocked(id, bootRetryDelay)
		n++
	}
	return n
}

// socketMayHaveAppeared reports whether a refusal could be the cache's
// staleness rather than the agent's state: a harness that speaks the socket,
// an agent with a session id, and no session for it in the snapshot.
func (e *Engine) socketMayHaveAppeared(l *core.Agent) bool {
	return harnessSpeaksSocket(l) && len(sessionsOf(l)) > 0 && !e.mightReachOverSocket(l)
}

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
	//
	// RE-ARMED, for the same reason maybeWake now defers rather than returning.
	// Returning here made the deferral a single extra look: an agent that called
	// Dibs once more in the meantime consumed the retry and the message was
	// stranded exactly as before, one window later. Only while somebody is still
	// blocked, which is what ends the loop.
	if e.recentlyInTouch(l) {
		if e.hasBlockingMail(agent) {
			e.deferWakeLocked(agent, e.recencyWindow(l))
		}
		return
	}
	if !e.hasBlockingMail(agent) {
		e.clearWakeAttempts(agent) // nothing owed: the next mail starts its own count
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
		// STILL NO SOCKET, STILL OWED. The first retry was armed for the
		// cache's staleness and a second miss returned without another,
		// while the question stayed pending: a socket that appeared later was
		// refreshed into the cache and the mail was never reconsidered. Keep
		// deciding at the refresh cadence for as long as blocking mail is
		// outstanding; nothing arms when there is none. Found by the
		// pre-release review, round sixteen.
		if e.socketMayHaveAppeared(l) {
			e.deferWakeLocked(agent, peerRecheckEvery)
		}
		return
	}
	stamp := e.wakeStamp(agent)
	cool := cmd.cooldown
	go func() {
		defer e.wakeExited(agent)
		n := e.noteWakeAttempt(agent)
		if e.runWake(cmd, agent) {
			e.clearWakeAttempts(agent)
			return
		}
		e.releaseWake(agent, stamp)
		if n < 2 {
			// The first execution for this mail, arrived here deferred; it
			// gets the one retry every first attempt is promised.
			e.deferWakeLocked(agent, cool)
		}
	}()
}

func (e *Engine) noteWakeAttempt(agent string) int {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if e.wakers.attempts == nil {
		e.wakers.attempts = map[string]int{}
	}
	e.wakers.attempts[agent]++
	return e.wakers.attempts[agent]
}

func (e *Engine) clearWakeAttempts(agent string) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	delete(e.wakers.attempts, agent)
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
	// NOT STRICTLY AFTER. Both timestamps come from time.Now(), and a check-in
	// immediately followed by the wake command exiting can read the same
	// instant: the turn end was then ignored and the finished turn went on
	// looking like a running one, so the next message was refused. It also made
	// TestTheWakeExitProducesBothOfItsFactsTogether fail once at the release
	// gate and pass two thousand times after, which is the shape of a race
	// nobody can reproduce on demand. Equal means the end is at least as recent
	// as the contact, and the end is the later fact by construction.
	if done, ok := e.turnEnded[l.ID]; ok && !done.Before(last) {
		return false
	}
	return !last.IsZero() && time.Since(last) < cmd.cooldown
}

// wakeFor picks the command for this agent and spends its cooldown.
//
// Split from maybeWake so the DECISION is testable without running anything: a
// test that has to spawn a process to find out whether it would have is a test
// nobody runs.
// argvFor is the operator's command for this agent's harness. Caller holds
// e.wakers.mu, and wakeRoute has already established that one exists.
func (e *Engine) argvFor(l *core.Agent) []string {
	return e.commandFor(l).argv
}

// commandFor is the operator's whole entry for this agent's harness, primary
// and fallback together. Caller holds e.wakers.mu.
func (e *Engine) commandFor(l *core.Agent) wakeCommand {
	harness := ""
	if l.Agent != nil {
		harness = l.Agent.Harness
	}
	return e.wakers.byHarness[strings.ToLower(harness)]
}

// wakeRoute decides HOW this agent would be reached, before asking whether it
// may be reached now.
//
// Split out of wakeFor because there are two routes and the choice between them
// is its own decision with its own reasons, none of which have anything to do
// with cooldowns. Caller holds e.wakers.mu.
//
// Returns the cooldown that route carries and whether a command is what will
// run; ok is false when neither route can reach this agent at all.
func (e *Engine) wakeRoute(l *core.Agent) (cool time.Duration, byCommand, ok bool) {
	harness := ""
	if l.Agent != nil {
		harness = l.Agent.Harness
	}
	cmd, found := e.wakers.byHarness[strings.ToLower(harness)]
	byCommand = found && len(cmd.argv) > 0

	// NO COMMAND IS NO LONGER NO WAKE.
	//
	// This used to return here, so an agent whose harness had no [wake.exec]
	// entry could not be woken at all. That was correct while spawning a process
	// was the only way to reach one, and it stopped being true: a harness that
	// publishes a per-session socket is reachable without the operator
	// configuring anything. The absence of a command is no longer the absence of
	// an address.
	//
	// No command AND no socket is still no wake, which is what the
	// operator-said-nothing guard asserts and what it should keep asserting.
	if !byCommand {
		if !e.mightReachOverSocket(l) {
			return 0, false, false
		}
		return defaultPeerCooldown, false, true
	}

	// THE THREAD IS THE EXEC PATH'S REQUIREMENT, NOT THE WAKE'S.
	//
	// `codex exec resume` needs a thread id to name, and the stdio bridge stores
	// `host-<ppid>` as the primary id while the resumable thread arrives
	// separately as an alias: a command run against the wrong one starts a
	// process that resolves no thread and leaves the mail unread. So the exec
	// path still refuses without one.
	//
	// A socket needs no thread, because the socket IS the address. Refusing here
	// used to refuse both, which made every agent with no thread id unwakeable
	// even while its session was listening: the single largest class of
	// unwakeable agent on this machine.
	if threadIDOf(l) == "" {
		slog.Debug("no wake by command: no harness thread id for this agent",
			"agent", l.ID, "session_id", l.SessionID)
		if !e.mightReachOverSocket(l) {
			return 0, false, false
		}
		return defaultPeerCooldown, false, true
	}
	return cmd.cooldown, true, true
}

func (e *Engine) wakeFor(l *core.Agent, msgType string, ev core.Event) (wakePlan, bool) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	cooldown, configured, ok := e.wakeRoute(l)
	if !ok {
		return wakePlan{}, false
	}
	thread := threadIDOf(l)
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
		return wakePlan{}, false
	}
	if last, seen := e.wakers.last[l.ID]; seen && now.Sub(last) < cooldown {
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
		e.deferWake(l.ID, cooldown-now.Sub(last))
		slog.Debug("no wake yet: inside the cooldown, re-checking when it expires",
			"agent", l.ID)
		return wakePlan{}, false
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
	f := wakeFields{
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
		//
		// AND THAT IS THE WHOLE SENTENCE. It used to continue "Call check_in,
		// then inbox, and act on anything there", which names two tools in order
		// and tells the model what to do with what it finds. That is steering,
		// and PHILOSOPHY rule 5 draws the line in exactly this place: the board
		// may WAKE an agent and may not decide what it does next. A wake that
		// arrives as a sequence of instructions is prompt injection with a
		// friendly justification, and the justification is the dangerous part
		// because it is the reason nobody re-read the sentence.
		//
		// What survives points at the channel and stops. An agent that has been
		// woken knows how to read its own mail; if it does not, that is a gap in
		// dibs://skills rather than something to fix one wake at a time. Found
		// by the pre-release review, which also noted the test for this only
		// required the word "board" and so passed the steering sentence.
		message: wakeNotice,
	}
	if !configured {
		// The socket carries the same sentence the command would have carried.
		// One notice, one wording, whichever way it travels.
		return wakePlan{
			agent: l.ID, sessions: sessionsOf(l), notice: f.message,
			cwd: cwdOf(l), cooldown: cooldown,
		}, true
	}
	// cwd ON THIS BRANCH TOO, and this is the branch that needs it.
	//
	// The socket return above has carried it since the field existed; this one,
	// the only route that runs a process and therefore the only one a working
	// directory means anything to, did not. So the field was assigned, and
	// looked used, on the path that cannot use it. `codex exec resume` then ran
	// in the daemon's directory and refused to start: "Not inside a trusted
	// directory", exit 1, which is every wake failure in this repository's log.
	//
	// Setting cmd.Dir was only half the fix and the half that tests cleanly.
	// The first version of that test called runWakeFor directly, so it passed
	// against a plan that never carried a directory at all.
	cmd := e.commandFor(l)
	return wakePlan{
		argv: f.apply(cmd.argv), fallback: f.apply(cmd.fallback),
		cwd: cwdOf(l), cooldown: cooldown,
	}, true
}

// wakePlan is how one wake will be delivered: the operator's command, or the
// harness's own per-session socket.
//
// A struct rather than an argv because there are now two ways to reach an
// agent and exactly one gate in front of them. Keeping the cooldown, the
// still-running flag and the deferral in one place is the whole point: those
// rules were each paid for by a bug, and a second delivery path that skipped
// them would re-buy every one.
type wakePlan struct {
	argv []string // the operator's command
	// fallback is the operator's second command, substituted like the first
	// and run only if the first exits non-zero. Empty when none is configured.
	fallback []string
	agent    string // whose wake this is, for the socket path
	notice   string // what to say; never a message body
	cwd      string // where the agent says it works, for the mismatch warning
	// cooldown is the rate limit THIS route carries.
	//
	// Carried rather than re-read, because re-reading it looked up the
	// operator's [wake.exec] entry and a socket route has none: the lookup
	// returned Go's zero duration, so a failed socket wake re-armed after the
	// 50ms margin instead of twenty seconds, on precisely the new
	// no-configuration path. wakeRoute already decided this; discarding its
	// answer and asking a map that was never going to have it is how the two
	// disagreed. Found by the pre-release review.
	cooldown time.Duration
	// sessions are every id this agent answers to, COPIED while the writer
	// loop holds still.
	//
	// The wake runs in a goroutine, and core.State is single-writer: reading
	// e.state.Agents or an agent's alias slice from that goroutine is a data
	// race against the loop, and a concurrent map access is a fatal crash
	// rather than a wrong answer. The first version of the socket route did
	// exactly that, and its tests missed it because they call the path against
	// quiescent state. Found by the pre-release review with a race probe.
	//
	// So the plan carries values, not a pointer into the board.
	sessions []string
}

// wakeNotice is every word a wake carries, on either route.
//
// A POINTER, NOT AN INSTRUCTION. It read "Dibs: check the board. Call check_in,
// then inbox, and act on anything there", which names two tools in order and
// says what to do with what they return: that is deciding what the agent does
// next, which PHILOSOPHY rule 5 forbids in the same breath as permitting the
// wake itself. Found by the pre-release review.
//
// "Check" rather than "you have mail" because a wake can be queued durably and
// land minutes later, by which time another activation may have read the mail:
// a resumed thread then finds an empty inbox and reasonably reports the wake as
// a lie. That happened in this feature's own testing. "Check" is true whenever
// it arrives.
//
// One constant, so both routes carry the same words and a test can read them
// without running a wake.
const wakeNotice = "Dibs: check the board."

// defaultPeerCooldown bounds socket wakes the way [wake.exec] entries bound
// process wakes. Shorter, because nothing is spawned: the cost of one is a
// connection and two lines, not an agent turn, so the rate limit is here to
// stop a burst becoming a stream of interruptions rather than to stop a fork
// bomb.
const defaultPeerCooldown = 20 * time.Second

// cwdOf is where this agent says it works, copied on the loop.
func cwdOf(l *core.Agent) string {
	if l == nil || l.Agent == nil {
		return ""
	}
	return l.Agent.CWD
}

// sessionsOf copies every name this agent answers to, primary first.
//
// A COPY. The caller is on the writer loop and the result outlives it: handing
// the goroutine l.SessionAliases itself would share a slice the loop goes on
// appending to.
func sessionsOf(l *core.Agent) []string {
	if l == nil {
		return nil
	}
	if l.CurrentSession != "" {
		// THE ACTIVATION TO REACH, AND ONLY THAT. The rest of the list was
		// appended after it as a fallback, and the socket route takes the
		// first address it finds: an agent that moved from A to B, with B
		// publishing no socket and A's still open, was woken at A, and a
		// delivery ends the attempt, so B stayed asleep on its mail. The
		// exec route stood down on the same case one round earlier; this one
		// does too. The scan below is for rows bound before the current
		// session was recorded. Found by the pre-release review, round
		// thirty-two.
		return []string{l.CurrentSession}
	}
	out := make([]string, 0, len(l.SessionAliases)+1)
	if l.SessionID != "" {
		out = append(out, l.SessionID)
	}
	out = append(out, l.SessionAliases...)
	return out
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
	// The one the harness reported most recently, when it is a thread. The
	// scan below is the older inference from append order, kept for rows
	// bound before the current one was recorded.
	if looksLikeThreadID(l.CurrentSession) {
		return l.CurrentSession
	}
	if l.CurrentSession != "" {
		// AND NOT THE THREAD BEFORE IT. The current activation is known and
		// is not a thread, so no thread is known for it: a persistent agent
		// recovered by nonce from a new `host-<ppid>` still held the uuid of
		// the activation it left, the scan below found it, and the wake
		// resumed the thread the agent had moved away from while the one
		// waiting stayed asleep. Found by the pre-release review, round
		// twenty-nine.
		return ""
	}
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

// looksLikeThreadID is core.LooksLikeThreadID: one definition of the shape.
func looksLikeThreadID(s string) bool { return core.LooksLikeThreadID(s) }

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
// runWake delivers one wake, by whichever route the plan names.
//
// Returns whether the agent was actually reached, which is what the caller's
// retry machinery turns on: a wake that failed spent no attempt and is still
// owed. That contract is why the socket path reports honestly rather than
// optimistically. Nothing here decides WHETHER to wake; that was settled under
// one lock in wakeFor.
func (e *Engine) runWake(plan wakePlan, agent string) bool {
	if len(plan.argv) > 0 {
		return runWakeCommands(plan.argv, plan.fallback, agent, plan.cwd, wakeTimeout, wakeGrace)
	}
	return e.wakeOverSocket(plan, agent)
}

// runWakeCommands runs the operator's command, and the fallback only if the
// first one fails.
//
// The primary's failure is still logged in full by runWakeFor, argv and
// directory included, because an operator whose primary is failing on every
// wake wants to know that even while the fallback is carrying the load. What
// follows says whether anything was tried next, so the two lines read as one
// story rather than a failure and an unexplained success.
//
// Measured, both halves, on the board this was written for: `codex exec
// resume` exit 1 with "already has an active writer" on a thread open in the
// desktop app, then `codex queue` exit 0, then the thread's own transcript
// carrying "Dibs: check the board." and the agent answering two questions it
// had been sent. The reverse case, a closed thread, is the one the primary
// already handled.
func runWakeCommands(argv, fallback []string, agent, dir string, timeout, grace time.Duration) bool {
	ok, out := runWakeForOut(argv, agent, dir, timeout, grace)
	if ok {
		return true
	}
	if len(fallback) == 0 {
		return false
	}
	// ONLY WHEN THE THREAD IS OPEN. The fallback exists for one failure: the
	// harness refusing to resume a thread its desktop app holds open. Run
	// after ANY failure, `codex queue` exited 0 on a closed thread whose
	// resume had failed for some other reason, parking the message where
	// nothing reads it, and that counted as a wake and suppressed the retry.
	// The primary's own words decide. Found by the pre-release review, round
	// twenty-three.
	if !openThreadFailure(out) {
		slog.Info("the wake command failed for a reason that is not an open thread; "+
			"the fallback would park the message, so it does not run",
			"agent", agent, "cmd", argv[0], "run_it_yourself", strings.Join(argv, " "))
		return false
	}
	slog.Info("the wake command found the thread open; trying the fallback",
		"agent", agent, "cmd", argv[0], "fallback", fallback[0])
	return runWakeFor(fallback, agent, dir, timeout, grace)
}

// openThreadMarkers are what the measured harness prints when it refuses to
// resume a thread something else holds open (codex 0.153: "thread-store
// conflict: thread <id> already has an active writer").
var openThreadMarkers = []string{"active writer", "thread-store conflict"}

func openThreadFailure(out []byte) bool {
	text := strings.ToLower(string(out))
	for _, m := range openThreadMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// runWakeFor is runWake with its bounds as arguments, so a test can assert that
// this RETURNS rather than assert that a constant is large. The old test
// checked only that wakeTimeout was at least two hours, which stays true while
// Wait blocks past it.
func runWakeForOut(argv []string, agent, dir string, timeout, grace time.Duration) (bool, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// #nosec G204 -- argv comes from the operator's own config file and nowhere
	// else: SetWakeCommands is the only writer, no tool or op reaches it, and
	// substitution replaces whole elements rather than building a string. There
	// is no shell in this path.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// IN THE AGENT'S DIRECTORY, WHICH IS THE WHOLE REASON THIS EVER WORKED.
	//
	// Without this the command inherits the DAEMON'S working directory, and a
	// daemon started by launchd has "/". Both documented wake commands care:
	// `codex exec resume` refuses outright there ("Not inside a trusted
	// directory"), exit 1, which is precisely the failure in this repository's
	// own daemon log, three times; and an agent resumed anywhere else would run
	// its next turn in the wrong tree even where the harness tolerates it.
	//
	// The plan has carried this value since the path shipped, set from the
	// agent's own record and commented as what it was for, and nothing read it.
	// That is worse than never having had it: the mechanism looked finished.
	//
	// Empty, or gone since the agent registered, means run where the daemon is
	// rather than refuse. A wake that might work beats one that certainly does
	// not, and the log says which happened so a failure is not a mystery.
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			cmd.Dir = dir
		} else {
			slog.Warn("the agent's directory is gone, so its wake runs where the "+
				"daemon does; a harness that resolves sessions per directory will "+
				"not find this one",
				"agent", agent, "cwd", dir, "err", err)
		}
	}
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
		// THE COMMAND, SO SOMEBODY CAN RUN IT THEMSELVES.
		//
		// The output stays withheld for the reason above, and that left
		// "exit status 1" and nothing else: an operator cannot act on that.
		// The argv is the operator's own config, so printing it discloses
		// nothing they did not write, and running it by hand is the one way to
		// see the output this deliberately will not log.
		fields = append(fields, "run_it_yourself", strings.Join(argv, " "))
		// AND WHERE IT RAN, because that is the difference that bites.
		//
		// This line used to blame the login keychain, and that was wrong. It
		// said a service cannot reach the operator's keychain or GUI session,
		// which sounded right and sent two investigations down a dead end. A
		// LaunchAgent probe in the identical domain and ProcessType as this
		// daemon read the login keychain and ran a complete `claude --resume`
		// turn, exit 0. The security session was never the problem.
		//
		// What actually differed was the working directory, now fixed above, so
		// the honest note names the directory the command really ran in and
		// leaves the diagnosis to whoever reads it.
		fields = append(fields, "ran_in", runDir(dir))
		// NO THEORY ABOUT WHY. Three have been wrong here.
		//
		// This line has carried a guess at the cause since it was written, and
		// the guess has misled every operator who read it, including the ones
		// who wrote it. First it blamed launchd's security session and the login
		// keychain, which a probe in the identical domain disproved. Then it
		// blamed the login shell's environment, and the real cause was a
		// thread-store conflict: the thread was open in the harness's desktop
		// app, which refuses a second writer, and no environment anywhere would
		// have changed that.
		//
		// The facts are useful and the theory is not. An operator has the argv
		// and the directory, which is enough to run it and see the real error in
		// under a minute; that is how the third wrong guess was caught. What
		// stays withheld is the command's OUTPUT, because a wake runs a whole
		// agent turn and that output is somebody's decrypted mail.
		if os.Getppid() == 1 {
			fields = append(fields,
				"note", "run the command above yourself to see what it said: its output "+
					"is withheld here because a wake runs a whole agent turn, and that "+
					"is somebody's decrypted mail")
		}
		slog.Warn("wake command failed; the next message somebody is blocked on "+
			"will try again", fields...)
		return false, out
	}
	slog.Info("woke an agent that was not running", "agent", agent, "cmd", argv[0])
	return true, out
}

// runWakeFor is runWakeForOut for callers that need the verdict alone.
func runWakeFor(argv []string, agent, dir string, timeout, grace time.Duration) bool {
	ok, _ := runWakeForOut(argv, agent, dir, timeout, grace)
	return ok
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

// isTheHuman reports whether an id is the human's own mailbox. Wake routes
// are for agents; a person is reached by the desktop notification the send
// path raises. The pull-only warning written for agents was attached to every
// send, and told a sender that nothing could wake "dibs web" and delivery
// waited on inbox or check_in, which misled the one decision that note exists
// to inform: whether to wait for a human's approval. Found by the pre-release
// review, round six.
func (e *Engine) isTheHuman(id string) bool {
	e.human.mu.Lock()
	defer e.human.mu.Unlock()
	return e.human.agent != "" && e.human.agent == id
}

// PullOnlyNoteFor is PullOnlyNote by agent id, read inside the loop.
func (e *Engine) PullOnlyNoteFor(ctx context.Context, agentID string) string {
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"note": e.PullOnlyNote(e.state.Agents[agentID])}
	})
	if err != nil {
		return "" // never fail a delivered send over an advisory note
	}
	n, _ := res["note"].(string)
	return n
}

// PullOnlyNote warns that mail to this agent will sit until somebody types.
//
// `send` already warns when the recipient is DORMANT: "it will see this when it
// next wakes". It said nothing when the recipient was ACTIVE on a harness with
// no wake path, which is the more misleading of the two, because an active row
// plus a silent ok reads as "this will arrive shortly" when in fact it arrives
// whenever a human next happens to type into that session. Measured: a request
// with a ninety-minute deadline went to an agent that had coordinated four
// minutes earlier, and nothing stirred.
//
// Nothing is broken when this fires. Some harnesses are pull-only by design,
// and Dibs will not spawn a process to drive one that has not asked
// (PHILOSOPHY rule 5). The defect was silence, not the absence of a wake.
//
// Here rather than in sleepingNote, which is where the other warning lives,
// because THIS one depends on the operator's `[wake.exec]` config. That is an
// impure input the fold must never read: it is not replayable, it changes
// without an op, and a note derived from it inside Apply would make replay
// depend on today's configuration file.
//
// AND IT SPEAKS FOR A SLEEPING AGENT TOO, which it used to refuse to do.
//
// The comment here read "empty when the agent is sleeping: core already says
// something better about that case". What core says is "it will see this when
// it next wakes", and when nothing can wake that agent, that is not something
// better. It is the one sentence the sender acts on, and it is false.
//
// The same shape the fold already fixed one branch over, for a message sent to
// an agent superseded by a live sibling, where the comment records that Dibs
// "told the senders it would be seen when it next wakes. Nobody was coming."
// This is that failure again, arrived at from the other direction: not a
// retired identity, but a live one nothing has a route to.
//
// Measured: a question sent to an idle codex agent with no thread id. Accepted,
// the sender told it would be seen when the agent next wakes, no wake attempted
// anywhere in the daemon log, and the message unread an hour later.
//
// Core cannot decide this and must not try: whether a wake is possible depends
// on the operator's `[wake.exec]` config, which is impure, not replayable, and
// changes without an op. So the engine's note wins wherever it has one, because
// it is the participant that knows.
func (e *Engine) PullOnlyNote(l *core.Agent) string {
	if l == nil || l.Gone() || e.isTheHuman(l.ID) {
		return ""
	}
	harness := wakeHarness(l)
	e.wakers.mu.Lock()
	cmd, ok := e.wakers.byHarness[harness]
	e.wakers.mu.Unlock()
	configured := ok && len(cmd.argv) > 0
	// CONFIGURED IS NOT THE SAME AS CAPABLE, and this asked only the first.
	//
	// wakeFor has a second mandatory condition: the agent must have a
	// UUID-shaped thread id for the resume command to name. Without one it
	// returns before starting anything. So an active agent whose harness HAS a
	// [wake.exec] entry, but which never supplied a thread id, got no wake and,
	// because this went quiet the moment an argv existed, no warning either:
	// precisely the silent success this note was added to remove, reintroduced
	// one condition further along. Found by the pre-release review, which also
	// caught that my own test fixture had no thread id and therefore pinned the
	// wrong behaviour while reading as if it proved the right one.
	if configured && threadIDOf(l) != "" {
		return "" // a wake can really run, so core's wording is true as it stands
	}
	named := harness
	if named == "" {
		named = "its harness"
	}
	// THE SOCKET ROUTE EXISTS, AND THIS NOTE SAID IT DID NOT. wakeRoute tries
	// a session socket when no command can run, and the bridge's own
	// subscription wakes a live Claude Code session; this told the sender
	// "nothing on this board can wake" an agent the daemon was about to nudge.
	// Best effort is still best effort, so the wording promises an attempt
	// and not an arrival. Found by the pre-release review, round three.
	socket := e.mightReachOverSocket(l)
	bestEffort := func(state, why string) string {
		return "delivered to " + l.ID + ", which is " + state + ". " + why + ", but a session " +
			"socket for it is open, so a best-effort notice will be tried. Nothing can confirm " +
			"it arrived: a session in bypassPermissions mode holds peer messages for its human. " +
			"If it is held, this is pull-only and arrives when that agent next calls inbox or " +
			"check_in."
	}
	// A SLEEPING AGENT NOTHING CAN REACH. Said plainly, because the alternative
	// is the sender believing a wake is coming.
	//
	// The socket route is not a rescue here the way it can be for an active
	// agent: it lives in the harness session, and this agent's session has
	// ended. Configured-and-nameable is the whole of what is left.
	if l.Sleeping() {
		why := "nothing on this board can wake " + named
		if configured {
			why = named + " has a wake command, but " + l.ID + " has never supplied " +
				"a harness thread id for it to resume"
		}
		if socket {
			// The command-side reason, worded as what is MISSING rather than as
			// "nothing can wake it", since the next clause says something will try.
			reason := "No wake command is configured for " + named
			if configured {
				reason = named + " has a wake command but this agent has never supplied a thread id for it"
			}
			return bestEffort(string(l.Status), reason)
		}
		return "delivered to " + l.ID + ", which is " + string(l.Status) + ", and " +
			why + ". Nothing will start it: this is NOT a message that will be seen " +
			"when it next wakes, because nothing is going to wake it. It waits until " +
			"a person starts that agent again. The message is not lost, and any " +
			"deadline on it will expire unread."
	}
	if socket {
		why := "No wake command is configured for " + named
		if configured {
			why = named + " has a wake command but this agent has never supplied a thread id for it"
		}
		return bestEffort("active", why)
	}
	if configured {
		return "delivered to " + l.ID + ", which is active, and " + named + " HAS a wake " +
			"command, but that agent has never supplied a harness thread id for it to " +
			"resume: the command needs one and will not run without it. So this is " +
			"pull-only in practice, and arrives when that agent next calls inbox or " +
			"check_in. If it is a harness that reports a thread id, it has not yet."
	}
	return "delivered to " + l.ID + ", which is active, but nothing on this board can " +
		"wake " + named + ": mail there is pull-only, so it arrives when that agent " +
		"next calls inbox or check_in, which may be when a person types into it. If you " +
		"set a deadline, that is the clock it is racing."
}

// wakeHarness is the table key for this agent, lowercased as SetWakeCommands
// stores it.
func wakeHarness(l *core.Agent) string {
	if l == nil || l.Agent == nil {
		return ""
	}
	return strings.ToLower(l.Agent.Harness)
}

// runDir names the directory a wake really ran in, for the failure log.
//
// Says "the daemon's own" rather than printing it, because the useful fact is
// that it was NOT the agent's: an operator reading "/" has to know what it was
// supposed to be before that means anything.
func runDir(dir string) string {
	if dir == "" {
		return "the daemon's own working directory (the agent recorded none)"
	}
	return dir
}

// recencyWindow is how long to wait before asking again whether this agent has
// stopped.
//
// The same window recentlyInTouch measures against, so a re-check lands when
// that judgement could actually have changed rather than at some unrelated
// interval. Falls back to the default peer cooldown for an agent whose harness
// has no configured command, which is the case recentlyInTouch already answers
// false for; carried anyway so this never returns zero and spins.
func (e *Engine) recencyWindow(l *core.Agent) time.Duration {
	harness := wakeHarness(l)
	e.wakers.mu.Lock()
	cmd, ok := e.wakers.byHarness[harness]
	e.wakers.mu.Unlock()
	if ok && cmd.cooldown > 0 {
		return cmd.cooldown
	}
	return defaultPeerCooldown
}
