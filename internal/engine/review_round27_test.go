package engine

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// [wake] sockets = false: the daemon's peer-socket route neither counts as
// reachability nor runs, whatever the cache holds.
func TestSocketsOffSwitchesThePeerSocketRouteOff(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	l := &core.Agent{
		ID: "cc", Name: "cc", Status: core.StatusActive, SessionID: sid,
		Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
		Slots: map[string]core.Slot{},
	}
	e.state.Agents["cc"] = l
	e.peers.mu.Lock()
	e.peers.at = time.Now()
	e.peers.live = map[string]peerwake.Session{sid: {PID: 4242, SessionID: sid, CWD: "/work"}}
	e.peers.mu.Unlock()
	if !e.mightReachOverSocket(l) {
		t.Fatal("setup: the socket route does not see this agent with sockets on, so the switch below proves nothing")
	}
	// A real listener, so an attempt to wake is visible as a connection.
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	conns := make(chan struct{}, 4)
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			conns <- struct{}{}
			_ = c.Close()
		}
	}()
	e.peers.mu.Lock()
	e.peers.live = map[string]peerwake.Session{sid: {PID: 4242, SessionID: sid, CWD: "/work", Socket: sock}}
	e.peers.mu.Unlock()
	_ = e.wakeOverSocket(wakePlan{sessions: []string{sid}, notice: "Dibs: check the board."}, "cc")
	select {
	case <-conns:
	case <-time.After(2 * time.Second):
		t.Fatal("setup: with sockets on, a socket wake made no connection, so the switch below proves nothing")
	}
	e.SetSocketWakes(false)
	if e.mightReachOverSocket(l) {
		t.Fatal("[wake] sockets = false and the socket route still counts the agent as reachable")
	}
	if e.wakeOverSocket(wakePlan{sessions: []string{sid}, notice: "Dibs: check the board."}, "cc") {
		t.Fatal("[wake] sockets = false and a socket wake reported success")
	}
	select {
	case <-conns:
		t.Fatal("[wake] sockets = false and the daemon still connected to the session's socket")
	case <-time.After(300 * time.Millisecond):
	}
}

// R27-1, from the dispatcher's side: an adoption's event for a recovered
// question is one the wake dispatcher acts on.
func TestAnAdoptedQuestionReachesTheWakeDispatcher(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.state.Agents["heir"] = &core.Agent{
		ID: "heir", Name: "heir", Status: core.StatusDormant, SessionID: sid,
		Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
		Slots: map[string]core.Slot{},
	}
	e.peers.mu.Lock()
	e.peers.at = time.Now()
	e.peers.live = map[string]peerwake.Session{} // scanned before the session existed
	e.peers.mu.Unlock()
	e.maybeWake(core.Event{
		Type: "message.adopted", To: "heir", Agent: "lost",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	e.wakers.mu.Lock()
	armed := e.wakers.deferred["heir"] != nil
	if armed {
		e.wakers.deferred["heir"].Stop()
	}
	e.wakers.mu.Unlock()
	if !armed {
		t.Fatal("the adoption's event for a recovered question did nothing at the wake dispatcher: " +
			"the heir is never told its recovered mail is waiting")
	}
}
