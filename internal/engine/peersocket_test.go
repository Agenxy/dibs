package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	// cwd included, because a sidecar has one and the mismatch warning compares
	// against it. Without it that warning can never fire and the test asserting
	// it stays quiet passes for the wrong reason.
	side, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sessionID, "cwd": "/w"})
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
			// DRAINED, not slammed shut. Closing on accept races the sender's
			// write and produces a broken pipe, which made a test about the
			// mismatch warning fail for an unrelated reason.
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
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

// The wake gate must never do discovery I/O, because it runs on the writer
// loop and the whole board waits behind it.
//
// The first version called the lookup that REFRESHES an expired cache, while
// its own comment said it read the cache and never refreshed. With a
// five-second TTL and a thirty-second background prime, most wake decisions did
// a directory scan and a `ps` per candidate inline on the loop, or blocked
// behind a goroutine holding the same mutex. A slow filesystem or a stuck `ps`
// stalls every board operation. Found by the pre-release review.
//
// Asserted by counting probes: the gate must run none.
func TestTheWakeGateDoesNoDiscovery(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "a", NewToken: "tok",
		SessionID: "some-session", Agent: &core.AgentInfo{Harness: "Claude Code"},
	}, t0Engine()); err != nil {
		t.Fatal("setup:", err)
	}

	var probes int
	prev := peerAlive
	peerAlive = func(int, string) bool { probes++; return true }
	t.Cleanup(func() { peerAlive = prev })

	// A warm cache, then let the TTL lapse so a refreshing lookup would fire.
	e.primePeerSessions()
	e.peers.mu.Lock()
	e.peers.at = time.Now().Add(-time.Hour)
	e.peers.mu.Unlock()
	probes = 0

	for i := 0; i < 5; i++ {
		e.mightReachOverSocket(st.Agents["a"])
	}
	if probes != 0 {
		t.Errorf("the gate ran %d liveness probe(s). Each one is a process spawned "+
			"while the single writer is held, so every other agent's declare, send "+
			"and check_in waits behind it", probes)
	}
}

// Priming happens before the loop serves, so the first wake of a daemon's life
// has a real answer rather than a cold cache.
//
// It was a goroutine racing the loop: an event arriving first was refused
// before any cooldown or deferral state existed, and a later prime only fills
// the cache, so nothing reconsidered mail that was already waiting. That wake
// was lost outright. Found by the pre-release review.
//
// SLOW ON PURPOSE. The first version simply asserted the cache was warm after
// one operation, and it passed against an ASYNC prime five times out of five,
// because a fast goroutine wins that race anyway. A test that cannot fail
// against the bug it names is worth less than no test. This one makes
// discovery take long enough that the ordering is decided rather than raced,
// and records whether priming had finished when the loop served.
func TestPrimingHappensBeforeTheLoopServes(t *testing.T) {
	sock, _ := listeningSession(t) // a real fixture, so discovery probes it
	go func() {
		for {
			c, err := sock.Accept()
			if err != nil {
				return
			}
			// DRAINED, not slammed shut. Closing on accept races the sender's
			// write and produces a broken pipe, which made a test about the
			// mismatch warning fail for an unrelated reason.
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()

	// HELD, not slept on. Discovery blocks inside the probe until this test
	// lets it go, so the ordering is decided by the code under test rather than
	// by whichever goroutine happens to be quicker.
	var once sync.Once
	probing, release := make(chan struct{}), make(chan struct{})
	prev := peerAlive
	peerAlive = func(int, string) bool {
		once.Do(func() { close(probing) })
		<-release
		return true
	}
	t.Cleanup(func() { peerAlive = prev })

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	select {
	case <-probing:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("discovery never probed the fixture session, so this test cannot " +
			"observe the ordering it exists for")
	}

	// Priming is in flight and held. A synchronous prime means the loop has not
	// started serving, so this must NOT complete.
	held, cancelHeld := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err := e.Do(held, &core.Op{
		Kind: core.OpRegister, Name: "early", Nonce: "n-early", AgentKind: core.KindPersistent,
	})
	cancelHeld()
	close(release)

	if err == nil {
		t.Error("the loop served an operation while discovery was still running. " +
			"An event arriving in that window is refused a socket wake before any " +
			"retry state exists, and nothing reconsiders it: the first socket " +
			"wake of this daemon's life is lost")
	}

	// And once priming is done the loop serves normally, with a warm cache.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "later", Nonce: "n-later", AgentKind: core.KindPersistent,
	}); err != nil {
		t.Fatalf("the loop never recovered after priming: %v", err)
	}
	if e.peerSnapshot() == nil {
		t.Error("the peer cache was still cold after priming completed")
	}
}

// A wake that lands in a session working somewhere else must say so.
//
// The wake goes wherever the binding points, and a binding can be wrong: a
// swept row frees a live session's id and the next agent registering in that
// directory inherits it. Before the socket route existed that misdelivery was
// invisible because the wake simply failed; now it succeeds, into the wrong
// session, which is more effective and no more correct.
//
// Logged rather than refused, deliberately. The daemon cannot tell which of the
// two is wrong, and a heuristic refusal would ground legitimate wakes for
// agents that genuinely moved. What this asserts is that it is not SILENT.
func TestAWakeIntoAnUnrelatedDirectoryIsReported(t *testing.T) {
	sock, sessionID := listeningSession(t)
	go func() {
		for {
			c, err := sock.Accept()
			if err != nil {
				return
			}
			// DRAINED, not slammed shut. Closing on accept races the sender's
			// write and produces a broken pipe, which made a test about the
			// mismatch warning fail for an unrelated reason.
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.primePeerSessions()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	// The fixture session reports /w; this agent says it works elsewhere.
	e.runWake(wakePlan{
		agent: "stranger", sessions: []string{sessionID},
		notice: "Dibs: check the board.", cwd: "/somewhere/entirely/else",
	}, "stranger")

	if got := buf.String(); !strings.Contains(got, "different working directory") {
		t.Errorf("a wake targeted a session whose directory does not match the "+
			"agent's, and nothing said so. That is a misdelivery nobody can find:\n  %q", got)
	}
}

// And it must REFUSE, not merely mention it.
//
// Delivering anyway interrupts a session that is not the recipient, leaves the
// intended agent asleep, and reports success, which spends the only attempt the
// retry machinery would have given it. Three failures at once, and the third is
// the "success with no effect" defect this release exists to remove. Found by
// the pre-release review.
func TestAWakeIntoAnUnrelatedDirectoryIsRefused(t *testing.T) {
	sock, sessionID := listeningSession(t)
	got := make(chan string, 1)
	go func() {
		c, err := sock.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		_ = c.Close()
		got <- string(b)
	}()

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.primePeerSessions()

	if e.runWake(wakePlan{
		agent: "stranger", sessions: []string{sessionID},
		notice: wakeNotice, cwd: "/somewhere/entirely/else",
	}, "stranger") {
		t.Error("a wake into a session working elsewhere reported SUCCESS. The " +
			"retry machinery now believes the agent was reached, so its one " +
			"attempt is spent while it is still asleep and somebody else was " +
			"interrupted instead")
	}
	select {
	case wire := <-got:
		t.Errorf("bytes were delivered to the wrong session: %q", wire)
	case <-time.After(300 * time.Millisecond):
		// nothing arrived, which is the point
	}
}

// And the ordinary case stays quiet, or the warning is noise and gets skimmed.
func TestAWakeIntoTheSameDirectoryIsQuiet(t *testing.T) {
	sock, sessionID := listeningSession(t)
	go func() {
		for {
			c, err := sock.Accept()
			if err != nil {
				return
			}
			// DRAINED, not slammed shut. Closing on accept races the sender's
			// write and produces a broken pipe, which made a test about the
			// mismatch warning fail for an unrelated reason.
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.primePeerSessions()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	e.runWake(wakePlan{
		agent: "resident", sessions: []string{sessionID},
		notice: "Dibs: check the board.", cwd: "/w", // the fixture's own cwd
	}, "resident")

	if got := buf.String(); strings.Contains(got, "different working directory") {
		t.Errorf("an ordinary wake was reported as a directory mismatch: %q", got)
	}
}

// The gate must not WAIT on a refresh either, which is a stronger claim than
// not performing one.
//
// The first fix removed discovery from the gate's call path and left the lock:
// peerSessions held peers.mu across a directory read and a `ps` per candidate,
// and peerSnapshot takes that same mutex, so the "I/O-free" gate on the writer
// loop still waited behind any concurrent refresh, up to 300ms per candidate.
//
// The test written for that fix asserted only that the gate ran no probe
// ITSELF. It never overlapped the gate with a refresh, so it could not see the
// contention its own comment described, and a changelog entry claimed wake
// decisions were free of discovery waits. Found by the pre-release review,
// which reproduced the wait.
//
// This holds a refresh open inside the probe and asks the gate meanwhile. It is
// event-driven: the gate answering is the signal, and a hung gate fails on the
// deadline rather than by sleeping.
func TestTheWakeGateDoesNotWaitOnARefresh(t *testing.T) {
	sock, _ := listeningSession(t)
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
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "a", NewToken: "tok",
		SessionID: "unrelated-session", Agent: &core.AgentInfo{Harness: "Claude Code"},
	}, t0Engine()); err != nil {
		t.Fatal("setup:", err)
	}

	// Warm it once with the real probe, then arrange for the NEXT refresh to
	// block inside discovery until this test releases it.
	e.primePeerSessions()
	var once sync.Once
	probing, release := make(chan struct{}), make(chan struct{})
	prev := peerAlive
	peerAlive = func(int, string) bool {
		once.Do(func() { close(probing) })
		<-release
		return true
	}
	t.Cleanup(func() { peerAlive = prev })

	e.peers.mu.Lock()
	e.peers.at = time.Now().Add(-time.Hour) // stale, so the next call refreshes
	e.peers.mu.Unlock()

	go func() { _ = e.peerSessions() }() // the refresh, which will block
	select {
	case <-probing:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("discovery never started, so the overlap this test needs did not happen")
	}

	answered := make(chan struct{})
	go func() {
		e.mightReachOverSocket(st.Agents["a"])
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("the wake gate blocked behind a discovery refresh. It runs on the " +
			"single-writer loop, so every other agent's declare, send and check_in " +
			"waits with it, for as long as a `ps` per candidate takes")
	}
	close(release)
}
