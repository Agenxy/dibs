package engine

import (
	"runtime"
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
