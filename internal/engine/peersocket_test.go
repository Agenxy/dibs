package engine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// A listening session is an address, so an agent is wakeable with no operator
// configuration at all.
//
// This is the change. [wake.exec] needs the operator to name a command AND
// needs Dibs to work out which thread id that command should resume, and every
// wake defect this project has had comes from the second half. A harness that
// publishes a per-session socket removes both: nothing to configure, and the
// address is read rather than inferred.
//
// The guards this sits beside still hold and still matter: no command and no
// socket is still no wake, and no process is ever spawned for a thread that
// cannot be resumed. What changed is that "no command" stopped meaning "no
// address".
func TestAnAgentWithNoConfiguredCommandIsWokenOverItsSocket(t *testing.T) {
	sock, sessionID := listeningSession(t)

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "sleeper", NewToken: "tok",
		SessionID: sessionID,
		Agent:     &core.AgentInfo{Harness: "Claude Code"},
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	l := st.Agents["sleeper"]

	// Setup must hold on both halves, or a pass below means nothing: no wake
	// command anywhere, and a session that really is discoverable.
	e.wakers.mu.Lock()
	nCommands := len(e.wakers.byHarness)
	e.wakers.mu.Unlock()
	if nCommands != 0 {
		t.Fatalf("setup: %d wake command(s) configured; this case is about having none", nCommands)
	}
	e.primePeerSessions()
	if _, ok := e.peerSessionFor(sessionsOf(l)); !ok {
		t.Fatal("setup: the fixture session was not discovered, so the wake below " +
			"would be refused for the wrong reason")
	}

	plan, ok := e.wakeFor(l, core.MsgQuestion, core.Event{
		Type: "message.sent", To: "sleeper", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if !ok {
		t.Fatal("an agent with a listening session and no configured command was " +
			"refused a wake. That is the state most agents on this machine are in, " +
			"and it is why mail went undelivered for a night")
	}
	if len(plan.argv) != 0 {
		t.Errorf("a command was planned where none is configured: %v", plan.argv)
	}
	if plan.notice == "" {
		t.Error("the plan carries no notice, so the socket wake would say nothing")
	}

	got := make(chan string, 1)
	go func() { got <- readAll(t, sock) }()
	if !e.runWake(plan, "sleeper") {
		t.Fatal("the socket wake reported failure")
	}
	wire := <-got
	if !strings.Contains(wire, `"type":"auth"`) {
		t.Errorf("no auth line was sent; the harness refuses an unauthenticated "+
			"connection: %s", wire)
	}
	if !strings.Contains(wire, plan.notice) {
		t.Errorf("the notice did not reach the wire: %s", wire)
	}
}

// A wake carries no message body, over this route as over the other.
//
// The exec path has a test for this because an argv is handed to arbitrary
// operator code. The socket path needs its own: it is a second door to the same
// room, and a rule enforced at one door is not enforced.
func TestASocketWakeCarriesNoMessageBody(t *testing.T) {
	sock, sessionID := listeningSession(t)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const secret = "SECRET the staging password is hunter2"
	for _, n := range []string{"sender", "sleeper"} {
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: n, NewToken: "tok-" + n,
		}, now); err != nil {
			t.Fatal("setup:", err)
		}
	}
	st.Agents["sleeper"].SessionID = sessionID
	st.Agents["sleeper"].Agent = &core.AgentInfo{Harness: "Claude Code"}
	if _, _, err := st.Apply(&core.Op{Kind: core.OpAckBoard, Token: "tok-sleeper"}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-sender", To: "sleeper",
		MsgType: core.MsgQuestion, Body: secret, DeadlineSec: 600,
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	e.primePeerSessions()

	plan, ok := e.wakeFor(st.Agents["sleeper"], core.MsgQuestion, core.Event{
		Type: "message.sent", To: "sleeper", Agent: "sender",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "sender"},
	})
	if !ok {
		t.Fatal("setup: no wake planned, so there is nothing to inspect")
	}
	got := make(chan string, 1)
	go func() { got <- readAll(t, sock) }()
	e.runWake(plan, "sleeper")
	wire := <-got
	if strings.Contains(wire, secret) || strings.Contains(wire, "hunter2") {
		t.Errorf("the message body was delivered to the harness socket. A wake says "+
			"that mail EXISTS; the agent reads it over the authenticated channel "+
			"with its own token, which is why mail is encrypted at rest:\n%s", wire)
	}
}

// listeningSession stands up what a harness publishes: a key, a sidecar, and a
// bound socket, and returns the socket and the session id.
func listeningSession(t *testing.T) (net.Listener, string) {
	t.Helper()
	const pid = 999001
	sessionID := "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal(map[string]string{"peerToken": strings.Repeat("a", 32)})
	name := strconv.Itoa(pid) + "." + strings.Repeat("b", 64) + ".key"
	if err := os.WriteFile(filepath.Join(dir, name), key, 0o600); err != nil {
		t.Fatal(err)
	}
	side, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sessionID})
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), side, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	// Short base: a unix socket path is bounded near 104 bytes and the default
	// temp dir on macOS is longer than that.
	run, err := os.MkdirTemp("/tmp", "pk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	t.Setenv("XDG_RUNTIME_DIR", run)
	if err := os.MkdirAll(filepath.Join(run, "cc-socks"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(run, "cc-socks", strconv.Itoa(pid)+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// The pid is invented, so liveness is stubbed for this fixture only. What
	// is under test here is the wake path, not the pid-reuse guard, which has
	// its own tests in internal/peerwake.
	peerwakeAliveForTest(t)
	return ln, sessionID
}

func readAll(t *testing.T, ln net.Listener) string {
	t.Helper()
	c, err := ln.Accept()
	if err != nil {
		return ""
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	b, _ := io.ReadAll(c)
	return string(b)
}

var _ = peerwake.Session{}

// peerwakeAliveForTest makes the fixture's invented pid discoverable, and puts
// the real check back afterwards.
func peerwakeAliveForTest(t *testing.T) {
	t.Helper()
	prev := peerAlive
	peerAlive = func(int, string) bool { return true }
	t.Cleanup(func() { peerAlive = prev })
}

// The wake goroutine must not read the board.
//
// core.State is single-writer. The socket wake runs in a goroutine, and the
// first version of it reached e.state.Agents from there and then read the
// agent's alias slice: a data race against the loop, and a concurrent map
// access is a fatal crash rather than a wrong answer. The socket tests missed
// it because they call the path against quiescent state, which is exactly the
// shape of test that cannot see a race.
//
// So this registers agents on the loop WHILE wakes run, under -race. The plan
// carries copied ids, so the goroutine has nothing to race on; if it ever
// reaches into the board again this fails or crashes.
func TestASocketWakeDoesNotRaceTheWriter(t *testing.T) {
	sock, sessionID := listeningSession(t)
	go func() {
		for {
			c, err := sock.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "sleeper", Nonce: "n-sleeper",
		AgentKind: core.KindPersistent, SessionID: sessionID,
		Agent: &core.AgentInfo{Harness: "Claude Code"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	e.primePeerSessions()

	plan := wakePlan{
		agent:    "sleeper",
		sessions: []string{sessionID},
		notice:   "Dibs: check the board.",
	}

	var wg sync.WaitGroup
	// Wakes, concurrently with writes.
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.runWake(plan, "sleeper") }()
	}
	// The writer, mutating the very fields the goroutine used to read: each
	// register binds a session and appends to an alias slice.
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = e.Do(ctx, &core.Op{
				Kind: core.OpRegister, Name: "churn" + strconv.Itoa(n),
				Nonce: "n-churn" + strconv.Itoa(n), AgentKind: core.KindPersistent,
				Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w"},
			})
		}(i)
	}
	wg.Wait()
}
