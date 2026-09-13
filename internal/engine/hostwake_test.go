package engine

import (
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
