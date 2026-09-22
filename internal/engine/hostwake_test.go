package engine

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// remoteAgent is an agent whose bridge said it is on another machine: the
// ordinary bridge shape (host-<ppid> primary, the harness thread as an alias)
// with a host id that is not the hub's.
const remoteThread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"

func remoteAgent(id, host string) *core.Agent {
	a := bridgeAgent(id, "Codex", remoteThread)
	a.Agent.HostID = host
	return a
}

func hubEngine() *Engine {
	return &Engine{state: core.NewState("hub-node", core.DefaultLimits())}
}

func questionFor(to string) core.Event {
	return core.Event{Type: "message.sent", To: to, Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"}}
}

// The hub's own command never runs for an agent on another machine. Before
// this, a remote agent whose harness the hub had a [wake.exec] entry for got
// that command started HERE, in a directory that is not here; it failed, the
// failure counted as the attempt, and the mail lost its retry.
func TestTheHubNeverRunsItsOwnCommandForAnAgentOnAnotherMachine(t *testing.T) {
	e := hubEngine()
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	far := remoteAgent("far", "laptop")
	if plan, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far")); ok {
		t.Fatalf("a wake was planned for an agent on another machine with no bridge there: %+v; "+
			"the hub's [wake.exec] is not a route to a directory on another computer", plan)
	}
	// The same agent, said to be on the hub's own machine, keeps the local route.
	near := remoteAgent("near", "hub-node")
	if plan, ok := e.wakeFor(near, core.MsgQuestion, questionFor("near")); !ok || plan.host != "" || len(plan.argv) == 0 {
		t.Fatalf("an agent on the hub's own machine lost its local command: ok=%v plan=%+v", ok, plan)
	}
}

// With that machine's bridge attached and claiming the harness, the wake is
// a request handed to the bridge, carrying exactly what its [wake.exec]
// entry may substitute, and the bridge's report is the exit status.
func TestARemoteAgentIsWokenThroughItsHostsBridge(t *testing.T) {
	e := hubEngine()
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	reqs, release, err := e.AttachHostBridge("laptop", []string{"Codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	far := remoteAgent("far", "laptop")
	plan, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
	if !ok {
		t.Fatal("no wake for a remote agent whose host has a bridge claiming its harness")
	}
	if plan.host != "laptop" || len(plan.argv) != 0 {
		t.Fatalf("plan = %+v: a remote wake carries the host and no argv; the hub never learns one", plan)
	}
	if r := plan.request; r.Agent != "far" || r.Harness != "codex" || r.Thread != remoteThread ||
		r.From != "asker" || r.MsgType != core.MsgQuestion || r.Notice != wakeNotice {
		t.Errorf("request = %+v: the bridge substitutes these into its operator's command, so each must be what f.apply would use here", r)
	}

	// Run it: the request reaches the bridge, and a good report is a wake.
	done := make(chan bool, 1)
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 5*time.Second) }()
	var got WakeRequest
	select {
	case got = <-reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge never received the request")
	}
	if got.ID == 0 || got.Thread != plan.request.Thread {
		t.Fatalf("the bridge received %+v", got)
	}
	if !e.ReportWakeResult(WakeResult{ID: got.ID, Host: "laptop", OK: true}) {
		t.Fatal("the hub was not waiting on the report it asked for")
	}
	if !<-done {
		t.Error("a bridge reporting success was recorded as a failed wake")
	}
	// A report nobody is waiting on is refused, so a bridge learns it was not heard.
	if e.ReportWakeResult(WakeResult{ID: got.ID, Host: "laptop", OK: true}) {
		t.Error("a second report on the same request was accepted")
	}

	// And a failure report is a failed wake, which the waker then defers
	// and retries exactly as a local command that exited non-zero.
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 5*time.Second) }()
	got = <-reqs
	e.ReportWakeResult(WakeResult{ID: got.ID, Host: "laptop", OK: false, Detail: "codex: exit 1"})
	if <-done {
		t.Error("a bridge reporting failure was recorded as a wake")
	}
}

// A bridge that stops reading, detaches, or never reports does not hold the
// wake forever: the request fails the way a command that failed to start does.
func TestARemoteWakeFailsWhenTheBridgeIsGoneOrSilent(t *testing.T) {
	e := hubEngine()
	far := remoteAgent("far", "laptop")
	reqs, release, err := e.AttachHostBridge("laptop", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
	if !ok {
		t.Fatal("setup: no plan")
	}
	// Silent: the bridge reads the request and never reports.
	done := make(chan bool, 1)
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 200*time.Millisecond) }()
	<-reqs
	if <-done {
		t.Error("a request nobody reported on counted as a wake")
	}
	// Detached mid-flight: the wait ends at once, as a failure.
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 10*time.Second) }()
	<-reqs
	start := time.Now()
	release()
	if <-done {
		t.Error("a request whose bridge detached counted as a wake")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("a detached bridge held the wake instead of failing it")
	}
	// Gone: no bridge, no route, and a plan that named one fails at once.
	if _, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far")); ok {
		t.Error("after the bridge detached the agent still had a route")
	}
	if e.requestRemoteWakeWithin(plan, "far", time.Second) {
		t.Error("a request for a host with no bridge counted as a wake")
	}
}

// One bridge per host: a reconnecting bridge replaces the stale stream, and
// a bridge that names no host is refused.
func TestAReconnectingBridgeReplacesTheStaleOne(t *testing.T) {
	e := hubEngine()
	if _, _, err := e.AttachHostBridge("  ", nil); err == nil {
		t.Error("a bridge with no host was attached")
	}
	old, releaseOld, _ := e.AttachHostBridge("laptop", []string{"codex"})
	cur, releaseNew, _ := e.AttachHostBridge("laptop", []string{"claude code"})
	defer releaseNew()
	if _, open := <-old; open {
		t.Error("the stale stream was not closed when its host reattached")
	}
	releaseOld() // the stale stream's deferred release must not detach the live one
	hosts := e.HostBridges()
	if len(hosts) != 1 || hosts[0].Host != "laptop" || len(hosts[0].Harnesses) != 1 || hosts[0].Harnesses[0] != "claude code" {
		t.Errorf("HostBridges = %+v, want the live bridge alone, with its own harness list", hosts)
	}
	select {
	case <-cur:
		t.Error("the live stream was closed by the stale one's release")
	default:
	}
}

// A pending wake belongs to one host: another host detaching does not fail
// it, and a report from the wrong host does not satisfy it.
//
// pending was keyed by id alone. Detaching host B sent a failure to every
// pending wake, including one executing on host A, and ReportWakeResult
// matched the id and never read res.Host, so a bridge could satisfy a wake it
// never ran. Found by the pre-release review.
func TestAPendingWakeIsBoundToItsHost(t *testing.T) {
	e := hubEngine()
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	reqsA, releaseA, err := e.AttachHostBridge("laptop", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	_, releaseB, err := e.AttachHostBridge("desktop", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	far := remoteAgent("far", "laptop")
	plan, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
	if !ok {
		t.Fatal("no wake for the remote agent")
	}
	done := make(chan bool, 1)
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 5*time.Second) }()
	var got WakeRequest
	select {
	case got = <-reqsA:
	case <-time.After(5 * time.Second):
		t.Fatal("laptop's bridge never received the request")
	}
	// The OTHER host's bridge goes away: laptop's wake is still running.
	releaseB()
	select {
	case r := <-done:
		t.Fatalf("laptop's wake finished with %v when desktop detached; it had not reported", r)
	case <-time.After(300 * time.Millisecond):
	}
	// The other host reports on laptop's id: not its wake to report.
	if e.ReportWakeResult(WakeResult{ID: got.ID, Host: "desktop", OK: true}) {
		t.Fatal("a report from desktop satisfied a wake laptop was running")
	}
	select {
	case r := <-done:
		t.Fatalf("the wrong host's report finished the wake with %v", r)
	case <-time.After(300 * time.Millisecond):
	}
	if !e.ReportWakeResult(WakeResult{ID: got.ID, Host: "laptop", OK: true}) {
		t.Fatal("laptop's own report was refused")
	}
	select {
	case r := <-done:
		if !r {
			t.Error("laptop reported success and the wake read as failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wake never finished after its host reported")
	}
}

// Wake ids are not reused across hub restarts.
//
// The counter started at zero on every start while a reconnecting bridge kept
// the ids of commands still running, so the restarted hub's request 1 was
// dropped as a duplicate of the old request 1, and the old command's report
// then satisfied the new request: a wake reported for an agent whose command
// never ran. Found by the pre-release review.
func TestWakeIDsDoNotRestartFromZero(t *testing.T) {
	first := hubEngine()
	second := hubEngine()
	for _, e := range []*Engine{first, second} {
		e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	}
	ids := map[uint64]bool{}
	for _, e := range []*Engine{first, second} {
		reqs, release, err := e.AttachHostBridge("laptop", []string{"codex"})
		if err != nil {
			t.Fatal(err)
		}
		far := remoteAgent("far", "laptop")
		plan, _ := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
		go func() { e.requestRemoteWakeWithin(plan, "far", 2*time.Second) }()
		select {
		case got := <-reqs:
			if ids[got.ID] {
				t.Fatalf("two hubs in a row handed a bridge the same wake id %d", got.ID)
			}
			ids[got.ID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("no request reached the bridge")
		}
		release()
	}
}

// A bridge that reconnects releases what its old connection was waiting on.
//
// Replacing a host's bridge closed the old stream and left its pending wakes
// in place: a request still unread in the old channel had nobody to run it,
// and its waiter held for the full wake timeout, suppressing further wakes
// for that agent. Failing them, as a detach does, costs at most one extra
// wake for a command that did run. Found by the pre-release review.
func TestAReplacedBridgeReleasesItsPendingWakes(t *testing.T) {
	e := hubEngine()
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	_, releaseOld, err := e.AttachHostBridge("laptop", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	far := remoteAgent("far", "laptop")
	plan, _ := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
	done := make(chan bool, 1)
	// Nobody reads the old stream: the request sits in its buffer, and the
	// waiter is registered as pending before it is sent, so waiting for the
	// pending entry is waiting for the request to be in flight.
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 10*time.Second) }()
	for {
		e.hostWakes.mu.Lock()
		n := len(e.hostWakes.pending)
		e.hostWakes.mu.Unlock()
		if n > 0 {
			break
		}
		runtime.Gosched()
	}
	_, releaseNew, err := e.AttachHostBridge("laptop", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseNew()
	releaseOld()
	select {
	case r := <-done:
		if r {
			t.Error("a wake nobody ran was reported as having run")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter was left holding after its bridge was replaced; nothing will ever report")
	}
}

// A BRIDGE ATTACHING RECONSIDERS THE MAIL ITS MACHINE'S AGENTS ARE OWED.
//
// A question for a remote agent that arrived while its machine's bridge was
// down was refused a wake ("no bridge there can start its harness"), and a
// refusal schedules no retry: the bridge reconnecting changed nothing, and
// the mail sat unwoken until some other event happened to reach that agent.
// The same order happens at every hub start, where the bridges attach after
// the boot retries have run. Attaching now arms the same retry boot does for
// every agent on that host holding blocking mail. Found by the pre-release
// review, round five.
func TestAnAttachingBridgeWakesTheMailItsAgentsWereOwed(t *testing.T) {
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})

	reg := func(name string, info *core.AgentInfo, session string) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Agent: info, SessionID: session})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatal("setup:", err)
		}
		return tok
	}
	reg("far", &core.AgentInfo{Harness: "Codex", HostID: "laptop", CWD: "/srv/work"}, remoteThread)
	asker := reg("asker", &core.AgentInfo{Harness: "Codex"}, "")

	// The question lands while no bridge for "laptop" is attached.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: asker, To: "far", MsgType: core.MsgQuestion, Body: "ready?", DeadlineSec: 600,
	}); err != nil {
		t.Fatal("setup:", err)
	}
	// The recipient has been away a while, as it would be after its bridge
	// went down: no recent contact, ephemeral or durable.
	_, _ = e.query(ctx, func() core.Result {
		delete(e.seen, "far")
		e.state.Agents["far"].LastCoordination = time.Now().Add(-time.Hour)
		return core.Result{}
	})

	reqs, release, err := e.AttachHostBridge("laptop", []string{"Codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	select {
	case got := <-reqs:
		if got.Agent != "far" {
			t.Errorf("the bridge was asked to wake %q, want far", got.Agent)
		}
		e.ReportWakeResult(WakeResult{ID: got.ID, Host: "laptop", OK: true})
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge attached and was never asked to wake the agent whose question " +
			"had been waiting: the refusal before the attach scheduled no retry")
	}
}

// A REMOTE AGENT THAT JUST CHECKED IN IS NOT WOKEN AGAINST ITS RUNNING THREAD.
//
// recentlyInTouch read the hub's own [wake.exec] entry to find the cooldown
// that defines "recently", and a remote agent's route is its host's bridge,
// which needs no entry here: with none, the agent was never recently in
// touch, so a question arriving a moment after its check-in had its bridge
// start a second resume against the thread it was working in, the duplicate
// this check exists to prevent. The route's cooldown is the bridge's. Round
// nine of the pre-release review.
func TestARemoteAgentJustInTouchIsNotWokenByItsBridge(t *testing.T) {
	e := hubEngine() // no [wake.exec] on the hub at all
	_, release, err := e.AttachHostBridge("laptop", []string{"Codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	far := remoteAgent("far", "laptop")
	far.Status = core.StatusActive
	e.seen = map[string]time.Time{"far": time.Now()} // checked in a moment ago
	if !e.recentlyInTouch(far) {
		t.Fatal("a remote agent that checked in a moment ago is not 'recently in touch' because the " +
			"hub has no local command for its harness: its bridge will start a second process against " +
			"the thread it is working in")
	}
}

// AND ITS HOST'S COOLDOWN IS THE ONE SPENT. A joined machine configured
// `cooldown = "30m"` for a harness was woken again after the hub's fixed
// ninety seconds: the bridge states its table's cooldowns on attach and the
// hub spends those. Round nine of the pre-release review.
func TestARemoteWakeSpendsTheHostsCooldown(t *testing.T) {
	e := hubEngine()
	_, release, err := e.AttachHostBridgeWith("laptop", []string{"Codex"},
		map[string]time.Duration{"codex": 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	far := remoteAgent("far", "laptop")
	plan, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
	if !ok {
		t.Fatal("no wake for a remote agent whose host has a bridge claiming its harness")
	}
	if plan.cooldown != 30*time.Minute {
		t.Errorf("the remote wake carries cooldown %v, want the joined machine's 30m", plan.cooldown)
	}
}

// THE DELIVERY NOTE DESCRIBES THE RECIPIENT'S ROUTE, not the hub's. For an
// agent on another machine that route is its host's bridge: with one
// attached and no hub command, the sender used to be told nothing could
// wake the recipient while its wake ran; with a hub command and no bridge,
// the sender was told nothing while nothing could reach it. Round nine of
// the pre-release review.
func TestThePullOnlyNoteReadsTheRemoteAgentsBridge(t *testing.T) {
	e := hubEngine() // no [wake.exec] on the hub
	far := remoteAgent("far", "laptop")
	far.Status = core.StatusDormant
	if note := e.PullOnlyNote(far); !strings.Contains(note, "no bridge attached") {
		t.Errorf("with no bridge for the host the note reads %q, want it to say no bridge is attached there", note)
	}
	_, release, err := e.AttachHostBridge("laptop", []string{"Codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if note := e.PullOnlyNote(far); note != "" {
		t.Errorf("with a bridge attached that can start its harness, the note reads %q, want none: the wake runs", note)
	}
	// And a hub command for the harness changes nothing for a remote agent.
	release()
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	if note := e.PullOnlyNote(far); !strings.Contains(note, "no bridge attached") {
		t.Errorf("with a hub command and no bridge the note reads %q: the hub's command is not a route to another machine", note)
	}
}

// A SUBSCRIPTION IS NOT CONTACT. The bridge opens one at registration and
// reopens it on every reconnect; it says the process is alive, not that the
// model is mid-turn. Counting it as contact put the agent "recently in
// touch" after its own Stop hook, and the question that arrived next was
// deferred for the whole cooldown against a turn that had ended. Round nine
// of the pre-release review, through the two-host suite.
func TestASubscriptionDoesNotCountAsTheAgentBeingInTouch(t *testing.T) {
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "resume", "{thread}"}}})
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "s", Agent: &core.AgentInfo{Harness: "Codex", CWD: "/w"}, SessionID: remoteThread,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.HookPoll(ctx, remoteThread, "Stop", "/w", false, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.SubscribeInfo(ctx, tok); err != nil {
		t.Fatal(err)
	}
	_, _ = e.query(ctx, func() core.Result {
		if e.recentlyInTouch(e.state.Agents["s"]) {
			t.Error("the bridge's subscription, opened after the agent's Stop, made the agent read as " +
				"mid-turn: its next question is deferred for the whole cooldown")
		}
		return core.Result{}
	})
}

// AND A BRIDGE IS NOT A ROUTE WITHOUT A THREAD TO NAME. wakeRoute refuses a
// remote wake when the agent has no harness thread id for the bridge's
// command to resume, and the note went quiet the moment a bridge was
// attached: an agent registered only under `host-<ppid>` was reported
// reachable while no wake could run. The local note already asks this
// question; the remote one now does too. Round seventeen of the
// pre-release review.
func TestThePullOnlyNoteForARemoteAgentNeedsAThreadAsWell(t *testing.T) {
	e := hubEngine()
	_, release, err := e.AttachHostBridge("laptop", []string{"Codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	far := bridgeAgent("far", "Codex", "") // host-<ppid> only, no thread
	far.Agent.HostID = "laptop"
	if _, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far")); ok {
		t.Fatal("setup: a wake was planned for an agent with no thread id, so the note below would be true")
	}
	note := e.PullOnlyNote(far)
	if note == "" || !strings.Contains(note, "thread") {
		t.Fatalf("the note reads %q for a remote agent with a bridge and no thread id: the sender is "+
			"told a wake is coming and nothing can run one", note)
	}
	// With a thread, the bridge really is the route, and the note stays quiet.
	if note := e.PullOnlyNote(remoteAgent("near", "laptop")); note != "" {
		t.Errorf("with a thread the note reads %q, want none", note)
	}
}

// A machine that adopted its Supgang identity still recognises the id it
// used to answer to.
//
// A machine that ran Dibs before joining the fleet has rows, claims and
// long-lived bridges carrying the id it minted. Adopting the fleet id at
// the next daemon start renamed the machine under all of them: the fold
// reads two ids as two machines, so the daemon's own agents read as
// remote, its own wake commands were skipped for them, and two agents on
// one computer could each take an exclusive claim on one path. The old id
// is recognised and replaced at ingress; nothing is ever stamped with it.
// Round thirty-five of the pre-release review.
func TestAnAdoptedIdentityStillRecognisesTheOldOne(t *testing.T) {
	const minted, fleet = "minted-1234", "a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152"
	e := New(core.NewState("hub-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHostID(fleet)
	e.SetHostAliases(minted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// A row registered before the adoption: this machine's, not a
	// stranger's. Arranged and read ON THE LOOP, which owns core.State.
	old := &core.Agent{
		ID: "old", Name: "old", Status: core.StatusActive,
		Agent: &core.AgentInfo{CWD: "/w/repo", HostID: minted},
	}
	var host string
	if _, err := e.query(ctx, func() core.Result {
		e.state.Agents["old"] = old
		host = e.remoteHostOf(old)
		return core.Result{}
	}); err != nil {
		t.Fatal(err)
	}
	if host != "" {
		t.Fatalf("a row registered under this machine's previous id reads as remote (%q): its "+
			"wakes go looking for a host bridge that will never exist", host)
	}

	// And a bridge still asserting the old id registers ONE machine: what
	// reaches the ledger is the identity in use.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "still-old", Agent: &core.AgentInfo{CWD: "/w/repo", HostID: minted},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)
	var stamped string
	if _, err := e.query(ctx, func() core.Result {
		stamped = e.state.Agents[id].Agent.HostID
		return core.Result{}
	}); err != nil {
		t.Fatal(err)
	}
	if got := stamped; got != fleet {
		t.Fatalf("a register asserting the previous id was recorded as %q: the board holds two "+
			"ids for one computer, and their claims stop colliding", got)
	}
	// A genuinely other machine is untouched.
	if got := e.canonicalHost("machine-b"); got != "machine-b" {
		t.Fatalf("another machine's id was rewritten to %q", got)
	}
}

// An announcement from a bridge that predates the identity adoption is
// this machine's, exactly as its hook poll is.
//
// A machine that joins the fleet adopts its Supgang id, the rows and
// claims here are renamed onto it, and the ingress recognises the id
// this computer used to answer to. HookPollFrom resolved that alias and
// NoteChildSession did not: a surviving bridge's announcement was filed
// under a machine no row is on, so hook_blocked answered ok with an
// empty parent and the next poll made a SECOND record for one session.
// Blocked state and progress then landed on a record nobody is attached
// to. Round forty-eight of the pre-release review.
func TestAnAnnouncementFromABridgeThatPredatesTheAdoptionIsThisMachines(t *testing.T) {
	const minted, fleet = "minted-1234", "fleet-id"
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetHostID(fleet)
	e.SetHostAliases(minted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// The bridge still asserts the id this computer used to answer to.
	if _, err := e.NoteChildSession(ctx, Child{
		SessionID: "host-12345", CWD: "/w/repo", Host: minted, State: "running",
	}); err != nil {
		t.Fatalf("announcement: %v", err)
	}
	// A later event from the SAME bridge, and the daemon's own view of
	// this machine, must land on one record and not two.
	if _, err := e.NoteChildSession(ctx, Child{
		SessionID: "host-12345", CWD: "/w/repo", Host: fleet, State: "blocked",
	}); err != nil {
		t.Fatalf("second announcement: %v", err)
	}

	var hosts []string
	for key := range e.children {
		hosts = append(hosts, key)
	}
	if len(e.children) != 1 {
		t.Fatalf("one session produced %d records (%v): the announcement carrying the old id "+
			"was filed under a machine no row is on, and everything recorded against it is "+
			"attached to nobody", len(e.children), hosts)
	}
	for _, c := range e.children {
		if c.Host != fleet {
			t.Fatalf("the record is filed under %q, want this machine's current id %q", c.Host, fleet)
		}
		if c.State != "blocked" {
			t.Fatalf("the later event did not reach the record: state %q", c.State)
		}
	}
}

// And asking about that session by the id the bridge still asserts finds
// it.
//
// Round forty-eight taught the WRITER to resolve an alias and left the
// READER asking with whatever it was handed. A bridge that survived this
// machine adopting its Supgang identity files its hooks under the new id,
// so `HookTrafficSeenOn` called with the old one found nothing: a fresh
// registration was told `hooks_live: false`, which reads as "nothing is
// waking this agent" and sends an operator to install a plugin that is
// already installed and working. Two call sites and one rule, and the one
// that reads was the one nobody swept, which is this repository's most
// expensive recurring shape. The key resolves its own aliases now. Round
// sixty-one of the pre-release review.
func TestHookTrafficIsFoundByAnIDThisMachineUsedToAnswerTo(t *testing.T) {
	const minted, fleet = "minted-1234", "fleet-id"
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetHostID(fleet)
	e.SetHostAliases(minted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// The hooks arrive from a bridge that predates the adoption.
	if _, err := e.NoteChildSession(ctx, Child{
		SessionID: "host-12345", CWD: "/w/repo", Host: minted, State: "running",
	}); err != nil {
		t.Fatalf("announcement: %v", err)
	}

	// Asked by either name, it is the same session.
	for _, host := range []string{minted, fleet, ""} {
		if !e.HookTrafficSeenOn(ctx, "host-12345", host) {
			t.Errorf("hook traffic for this session is invisible when asked about host %q: "+
				"a working guard reports hooks_live false, and the operator is told to "+
				"install a plugin that is already installed", host)
		}
	}
	// And a session nobody announced is still unseen: this is not a test
	// that passes because everything answers yes.
	if e.HookTrafficSeenOn(ctx, "host-99999", fleet) {
		t.Error("a session nothing announced was reported as having hook traffic")
	}
	// Nor does another machine's session leak into this one's answer.
	if e.HookTrafficSeenOn(ctx, "host-12345", "some-other-machine") {
		t.Error("another machine's session id matched this machine's record: `host-<ppid>` " +
			"repeats across computers, which is why the key carries the host at all")
	}
}
