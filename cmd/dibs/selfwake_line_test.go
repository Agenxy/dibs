package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

// The in-session route says what the daemon computed, and has nothing of its
// own to say.
//
// THE COMPLAINT WAS THE EXTRA CARD, not the wording of it. A Claude Code
// session has one message socket and Dibs wrote to it twice per message: this
// bridge, in-process, with a fixed content-free sentence, and the daemon,
// through the harness's session sidecar, with the digest. The bridge won the
// race every time, so the operator's screen showed a placeholder wrapped in the
// harness's peer-message preamble and then the real thing underneath. Three
// releases reworded the placeholder. This one deletes it: the daemon puts the
// digest on the notification, the bridge sends that, and the daemon stands down
// for as long as this bridge is subscribed.
//
// Which makes the empty case the interesting one. No digest means a daemon older
// than the key, and that daemon has NOT stood down: it is writing its own notice
// to this same socket. A line from here would be the duplicate again, carrying
// nothing. So the bridge says nothing, and the test that matters is that it
// invents no substitute.
func TestTheInSessionWakeSendsTheDaemonsDigestAndNothingOfItsOwn(t *testing.T) {
	const digest = "mail for your agent \"worker\".\n  #7 from \"asker\" (question): does the fold hold?"
	if got := selfWakeLine(map[string]any{mcp.DigestMetaKey: digest}); got != digest {
		t.Errorf("the in-session wake did not send the digest it was handed.\n got: %q\nwant: %q",
			got, digest)
	}
	// NO DIGEST, NO LINE. Every earlier version answered this case with a
	// sentence of its own, and every one of those sentences was the card the
	// operator was complaining about.
	for _, meta := range []map[string]any{
		nil,
		{},
		{mcp.MsgTypeMetaKey: "question"},
		{mcp.EventMetaKey: "message.sent", mcp.MsgTypeMetaKey: "question"},
		{mcp.DigestMetaKey: ""},
	} {
		if got := selfWakeLine(meta); got != "" {
			t.Errorf("with no digest in the notification the bridge composed a line of its "+
				"own: %q. An older daemon is still writing its own notice to this same "+
				"socket, so this line is the second card, and it is the one with no "+
				"information in it", got)
		}
	}
}

// And no placeholder anywhere in the file, which is the operator's instruction
// stated as a test rather than as a memory.
//
// A GUARD ON THE SHAPE, because the wording was never the problem and a guard on
// the wording is what let this survive three releases. Each round retired one
// sentence and wrote a test that the retired one was gone, and the next round
// added a new sentence that passed it. This looks for the sending, not the
// spelling: nothing in the in-session wake path may hand the waker a string
// this process made up.
func TestTheBridgeInventsNoNoticeOfItsOwn(t *testing.T) {
	src, err := os.ReadFile("selfwake_watch.go")
	if err != nil {
		t.Fatal(err)
	}
	// The one call that reaches the session socket on this path takes `line`,
	// and `line` comes from selfWakeLine, which returns what the daemon sent
	// or "". A literal argument here is a sentence this process composed.
	if !strings.Contains(string(src), "waker.wake(line)") {
		t.Error("the in-session wake no longer sends the line selfWakeLine returned. " +
			"If this call was changed, check what it sends now: a string literal or a " +
			"fmt.Sprintf here is a notice this bridge made up, which is the duplicate " +
			"card the operator asked four times to have removed")
	}
	if strings.Contains(string(src), `wakeexec.Compose`) {
		t.Error("the in-session route composes a notice again. Compose exists for the " +
			"exec route, whose argv is world-readable and which genuinely has no digest " +
			"to send; this route is handed one")
	}
}

// A bridge that cannot deliver hands the socket back, or nobody writes at all.
//
// THE HOLE THE ONE-WRITER RULE OPENS, found by dibs-coordinator asking the
// right question one step short of its answer. The daemon stands down for an
// agent whose bridge declared it can reach its own session. Declaring is
// evidence of CAPABILITY, not of delivery: a bridge whose socket has stopped
// accepting leaves the daemon quiet by arrangement and itself failing into the
// dark, so the mail is announced by nothing. That is the oldest failure shape
// in this repository, reports success while doing nothing, reintroduced by the
// change that removed a duplicate.
//
// AT ONCE, because the error said the socket is gone. The first cut counted to
// two instead, and dibs-coordinator argued the discriminator should be the
// errno rather than the frequency: a dead socket and a flaky one differ in
// WHICH error, and the dead one is the common case, so counting paid a fifteen
// second delay on every ordinary failure to guard an unusual one.
func TestASocketThatIsGoneSurrendersTheRouteAtOnce(t *testing.T) {
	// SHORT BASE, and the long name of this test is why. A unix socket path is
	// bounded near 104 bytes and macOS's temp dir plus a descriptive test name
	// is longer, so filepath.Join(t.TempDir(), ...) makes the dial fail with
	// EINVAL rather than ENOENT. EINVAL is not "the socket is gone", so the
	// surrender correctly did not fire and the test reported the product
	// broken. The fourth broken probe of the night, and peersocket_test.go
	// already carries this exact warning.
	dir, err := os.MkdirTemp("/tmp", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "gone.sock")
	if len(sock) > 100 {
		t.Fatalf("setup: socket path is %d bytes, so the dial below fails with EINVAL "+
			"and proves nothing about a socket that is gone", len(sock))
	}
	w := &selfWaker{socket: sock, token: "t", cooldown: time.Hour}
	if !w.canReach() {
		t.Fatal("setup: a fresh waker already claims it cannot reach its session")
	}
	// A socket path that does not exist is ENOENT: unambiguous, and what the
	// end of a session actually looks like.
	if err := w.wake("first"); err == nil {
		t.Fatal("setup: writing to a socket that does not exist succeeded")
	}
	if w.canReach() {
		t.Error("the bridge still claims the route after its socket turned out not to " +
			"exist. The daemon stands down on that claim, so nothing at all announces " +
			"this agent's mail until the session is restarted")
	}
	// And the next listen must say so, or the daemon goes on standing down.
	iw := &inboxWatcher{waker: w}
	body := string(iw.listenBody(&inboxStream{key: "k", token: "tok"}))
	if strings.Contains(body, mcp.SelfWakeMetaKey) {
		t.Errorf("a surrendered bridge still declares it delivers its own wakes: %s", body)
	}
}

// Which errors mean gone, and which only mean not right now.
//
// The list is deliberately short, like dialFailed one file over. A timeout is
// the case the retry exists for: it does not distinguish a dead session from a
// busy one, and surrendering on it would hand the daemon a route that a
// bypassPermissions session HOLDS, costing an agent every wake it would have
// had. That cost is why the count survives for the ambiguous errors instead of
// being deleted outright.
func TestOnlyAnAbsentPeerCountsAsAGoneSocket(t *testing.T) {
	gone := []error{syscall.ENOENT, syscall.ECONNREFUSED, syscall.EPIPE, syscall.ECONNRESET}
	for _, err := range gone {
		if !socketGone(err) {
			t.Errorf("%v does not count as a gone socket, but it says the other end is "+
				"absent: the bridge would wait fifteen seconds and retry into nothing "+
				"while the daemon stays quiet", err)
		}
		// Wrapped the way net and os hand them back.
		if !socketGone(&net.OpError{Op: "dial", Err: err}) {
			t.Errorf("%v wrapped in a net.OpError stopped counting: that is the form "+
				"the dialler actually returns", err)
		}
	}
	ambiguous := []error{syscall.ETIMEDOUT, syscall.EAGAIN, errors.New("something else")}
	for _, err := range ambiguous {
		if socketGone(err) {
			t.Errorf("%v was treated as proof the socket is gone. It is not, and an "+
				"unnecessary surrender hands the route to the daemon, whose write a "+
				"bypassPermissions session holds", err)
		}
	}
	if socketGone(nil) {
		t.Error("a nil error was read as a failure")
	}
}

// A nil waker is the ordinary case for every harness that publishes no socket,
// and it must never claim the route. This is the path the ChatGPT app and the
// per-call plugins take.
func TestAHarnessWithNoSocketClaimsNothing(t *testing.T) {
	iw := &inboxWatcher{}
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "")
	body := string(iw.listenBody(&inboxStream{key: "k", token: "tok"}))
	if strings.Contains(body, mcp.SelfWakeMetaKey) {
		t.Errorf("a bridge with no session socket told the daemon to stand down: %s.\n"+
			"  Nothing would then announce this agent's mail: no socket here, and the "+
			"daemon quiet by arrangement", body)
	}
}

// The ambiguous path surrenders too, after the retry, and this DRIVES it.
//
// A COUNTER WOULD NOT HAVE. The first attempt at guarding this counted
// `w.surrender()` call sites in the source, and dibs-coordinator took it apart
// in a line: #245's test entered at the flag, so it tested everything
// downstream of the flag and nothing that sets it, and a call-site counter
// enters at the AST, so it asserts a wire-shaped object exists rather than
// that the wire carries current. It is satisfied by a call in a branch that
// never runs, which is a SUPERSET of the bug it was written for, because dead
// code with a call site is still dead. A guard has to enter through the same
// door as production, and for a wake that door is a failed write.
//
// ENOTSOCK is the fixture because it is deliberately absent from socketGone: a
// path that is not a socket is a misconfiguration rather than a session that
// ended. If it is ever added there, this test needs a different ambiguous
// error, and it will say so by failing on the first assertion rather than the
// last.
func TestAnAmbiguousFailureSurrendersOnlyAfterTheRetry(t *testing.T) {
	t.Cleanup(func() { recordWakePending(false, "") })
	// THE ERROR IS INJECTED, AND THE CI GATE IS WHY. The first version dialled
	// a path that is not a socket, which answers ENOTSOCK on macOS and
	// ECONNREFUSED on Linux; the second is in socketGone, so the test
	// exercised the immediate surrender on one machine and the retry on the
	// other. Its own setup assertion caught that rather than letting it pass
	// for the wrong reason, which is the argument for asserting your setup.
	// What this branch is about is what the code does with an ambiguous error,
	// not which error a kernel happens to pick, so the error is stated here.
	var attempts atomic.Int32
	w := &selfWaker{
		socket: "unused", token: "t", cooldown: 120 * time.Millisecond,
		deliverFn: func(string) error {
			attempts.Add(1)
			return syscall.ETIMEDOUT // ambiguous on every platform: not in socketGone
		},
	}
	if socketGone(syscall.ETIMEDOUT) {
		t.Fatal("setup: ETIMEDOUT now counts as a gone socket, so this exercises the " +
			"IMMEDIATE surrender and proves nothing about the retry path")
	}
	if err := w.wake("owed"); err == nil {
		t.Fatal("setup: the injected failure was reported as a delivery")
	}
	// Not yet: one ambiguous failure might have been momentary, and giving the
	// route back costs an agent every wake the daemon's route cannot deliver.
	if !w.canReach() {
		t.Error("an ambiguous failure surrendered on the first attempt. The daemon's " +
			"route is the one a bypassPermissions session HOLDS, so a momentary error " +
			"must not cost it")
	}
	// The retry fires on its own timer and fails the same way; THAT is evidence.
	deadline := time.Now().Add(4 * time.Second)
	for w.canReach() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if w.canReach() {
		t.Error("two ambiguous failures and the bridge still claims the route: the " +
			"daemon stays quiet for this agent and nothing announces its mail")
	}
	if n := attempts.Load(); n < 2 {
		t.Errorf("the write was attempted %d time(s); the retry is what makes the "+
			"second failure evidence, so one attempt means the surrender came from "+
			"somewhere else", n)
	}
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
}
