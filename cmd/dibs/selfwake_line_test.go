package main

import (
	"os"
	"path/filepath"
	"strings"
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
// Surrender is not free either, which is why it takes two failures. The
// daemon's route is the one a session in bypassPermissions holds, so a single
// transient error must not cost it. Two attempts fifteen seconds apart is
// evidence rather than noise.
func TestABridgeThatCannotDeliverHandsTheSocketBack(t *testing.T) {
	w := &selfWaker{socket: filepath.Join(t.TempDir(), "gone.sock"), token: "t", cooldown: time.Hour}
	if !w.canReach() {
		t.Fatal("setup: a fresh waker already claims it cannot reach its session")
	}
	// A first failure alone must NOT surrender: the daemon's route is held in
	// bypassPermissions mode, and giving it back on one hiccup can cost an
	// agent every wake it would have had.
	if err := w.wake("first"); err == nil {
		t.Fatal("setup: writing to a socket that does not exist succeeded")
	}
	if !w.canReach() {
		t.Error("one failed delivery surrendered the route. The daemon's route is the " +
			"one a bypassPermissions session holds, so this trades a duplicate for a " +
			"session that may hear nothing at all")
	}
	// The retry failing is the second attempt, and that is evidence.
	w.surrender()
	if w.canReach() {
		t.Fatal("surrender did not take")
	}
	// And the next listen must say so, or the daemon goes on standing down for
	// a bridge that has stopped delivering.
	iw := &inboxWatcher{waker: w}
	body := string(iw.listenBody(&inboxStream{key: "k", token: "tok"}))
	if strings.Contains(body, mcp.SelfWakeMetaKey) {
		t.Errorf("a surrendered bridge still declares it delivers its own wakes: %s.\n"+
			"  The daemon reads that and stays quiet, and nothing announces this "+
			"agent's mail", body)
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
