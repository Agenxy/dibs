package main

import (
	"os"
	"strings"
	"testing"

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
