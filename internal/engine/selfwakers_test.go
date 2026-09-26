package engine

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// ONE WRITER TO A SESSION SOCKET, AND THE OPERATOR SAW TWO.
//
// A Claude Code session has exactly one message socket. Dibs had two
// independent things writing to it: this daemon, which finds it through the
// harness's ~/.claude/sessions sidecar and sends the digest, and the session's
// own stdio bridge, which finds it through CLAUDE_CODE_MESSAGING_SOCKET and
// sent a fixed sentence with no facts in it. Neither knew the other existed.
// The bridge is in-process and skips the sidecar lookup, so it won every race:
// the operator's screen showed the placeholder first, then the real digest,
// each wrapped in the harness's own "another Claude session" preamble. They
// reported it four times. Three releases reworded the placeholder. None of them
// counted the writers.
//
// So a bridge that can reach its own session says so when it subscribes, and
// this daemon stands down for that agent while it is there. The direction is
// forced rather than chosen: the bridge KNOWS whether it has a socket, and a
// self-sent message is accepted where a stranger's is held in
// bypassPermissions mode, so it is both the better informed party and the more
// reliable route. This daemon cannot even tell a held message from a delivered
// one.
func TestTheDaemonDoesNotWriteToASocketAnAgentsOwnBridgeIsServing(t *testing.T) {
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
	e.primePeerSessions()
	if _, ok := e.peerSessionFor(sessionsOf(l)); !ok {
		t.Fatal("setup: the fixture session was not discovered, so a quiet socket below " +
			"would prove nothing about standing down")
	}
	plan, ok := e.wakeFor(l, core.MsgQuestion, core.Event{
		Type: "message.sent", To: "sleeper", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if !ok || plan.notice == "" {
		t.Fatal("setup: no socket wake was planned, so this proves nothing")
	}

	// ONE ACCEPT LOOP, not a reader goroutine per phase. Two readers on one
	// listener is a broken probe: the reader armed for the silence accepts the
	// NEXT phase's connection, and the phase that should see a write sees
	// nothing. That produced a confident failure about a bug that was not there.
	conns := make(chan string, 4)
	go func() {
		for {
			c, err := sock.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			b, _ := io.ReadAll(c)
			_ = c.Close()
			conns <- string(b)
		}
	}()

	// THE PROBE FIRST. With nobody claiming the session, the same plan writes
	// to the same socket, so the silence below is the stand-down and not a
	// fixture that was never listening.
	if !e.runWake(plan, "sleeper") {
		t.Fatal("setup: the socket wake failed with no bridge attached")
	}
	select {
	case wire := <-conns:
		if !strings.Contains(wire, `"type":"auth"`) {
			t.Fatalf("setup: the unclaimed wake wrote nothing usable: %s", wire)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("setup: nothing reached the fixture socket with no bridge attached, so " +
			"the silence below would prove nothing")
	}

	// Now the agent's own bridge is subscribed and says it delivers its own.
	release := e.AttachSelfWaker("sleeper", sessionID)
	// The same plan again: runWake carries no cooldown of its own, the gate
	// being in wakeFor, so this is the delivery and nothing else.
	if !e.runWake(plan, "sleeper") {
		t.Error("standing down was reported as a FAILED wake. It is not a failure: the " +
			"notice is going out over the route that does not have to guess whether the " +
			"receiver will accept it, and reporting failure here arms a retry that would " +
			"write to the socket anyway")
	}
	select {
	case wire := <-conns:
		t.Errorf("the daemon wrote to a session its own bridge is serving: %s.\n"+
			"  That is two notifications for one message, which is what the operator "+
			"was looking at, and the bridge's arrives first because it needs no "+
			"sidecar lookup", wire)
	case <-time.After(400 * time.Millisecond):
	}

	// AND IT COMES BACK. A bridge that dies must not take the socket route with
	// it: the claim lasts exactly as long as the subscription.
	release()
	if live, _ := e.SelfWaking("sleeper"); live {
		t.Fatal("setup: the claim survived its release, so the check below is moot")
	}
	if !e.runWake(plan, "sleeper") {
		t.Fatal("the socket wake failed after the bridge released its claim")
	}
	select {
	case wire := <-conns:
		if !strings.Contains(wire, `"type":"auth"`) {
			t.Errorf("the resumed wake wrote nothing usable: %s", wire)
		}
	case <-time.After(2 * time.Second):
		t.Error("nothing was written after the bridge went away: a subscription that " +
			"ends must hand the socket route back, or a crashed bridge silences the " +
			"one route left to an idle session")
	}
}

// A reconnecting bridge overlaps its streams, and the socket must not come back
// in the gap.
//
// A COUNT, NOT A FLAG. The bridge re-subscribes before the old stream's release
// runs, which is the ordinary shape of a reconnect: with a flag, the old
// stream's release clears a claim the new stream still holds, and the next wake
// goes to the socket while a perfectly good bridge is attached. That is the
// duplicate again, arriving only on reconnects, which is the kind of thing this
// project finds in a screenshot six weeks later.
func TestAReconnectingBridgeKeepsItsClaimWhileItOverlaps(t *testing.T) {
	e := New(core.NewState("t", core.DefaultLimits()), &memLedger{}, deadProber{})
	first := e.AttachSelfWaker("worker", "session-a")
	second := e.AttachSelfWaker("worker", "session-a") // reconnected before the old one released
	first()
	if live, _ := e.SelfWaking("worker"); !live {
		t.Error("the reconnect lost the claim when the OLD stream released it: the daemon " +
			"would write to the socket the new stream is already serving")
	}
	second()
	if live, _ := e.SelfWaking("worker"); live {
		t.Error("the claim outlived every subscription that made it, so the socket route " +
			"is gone for good and an idle session has none left")
	}
	// Releasing twice is what a deferred release plus an explicit one looks
	// like, and must not go negative and strand the claim.
	second()
	if live, _ := e.SelfWaking("worker"); live {
		t.Error("a repeated release re-armed the claim")
	}
}

// The digest an agent is owed, read without consuming it.
//
// THE HAZARD THIS EXISTS TO AVOID. The obvious way for a bridge to learn what
// arrived is to call inbox or hook_poll, and both MARK MAIL DELIVERED. A bridge
// that asks and then fails to write to its session has spent a delivery nobody
// saw, which is this repository's most expensive recurring bug. So the daemon
// computes the digest on the way out and hands it over on the notification.
func TestReadingTheWakeDigestConsumesNothing(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, aliveProber{})
	// REAL TIME AND A LONG DEADLINE. The fixed fixture clock puts the message's
	// deadline in the past the moment the engine's own sweep runs against
	// time.Now(), so the mail expires as recipient-dead and the assertion below
	// reads that as the digest having consumed it. A confident failure about a
	// bug that was not there: the probe, not the product.
	now := time.Now()
	for _, op := range []*core.Op{
		{Kind: core.OpRegister, Name: "worker", NewToken: "worker-tok"},
		{Kind: core.OpRegister, Name: "asker", NewToken: "asker-tok"},
		{
			Kind: core.OpSendMessage, Token: "asker-tok", To: "worker", MsgType: core.MsgQuestion,
			Body: "does the fold hold?", DeadlineSec: 3600,
		},
	} {
		if _, _, err := st.Apply(op, now); err != nil {
			t.Fatal("setup:", err)
		}
	}
	before := st.Inbox("worker")
	if len(before) != 1 || before[0].State != core.MsgStatePending {
		t.Fatalf("setup: the mailbox holds %d message(s) in an unexpected state; this test "+
			"is about a read not moving one", len(before))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	digest, err := e.WakeDigestFor(ctx, "worker-tok", "asker", string(core.MsgQuestion))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(digest, "asker") {
		t.Errorf("the digest does not name the sender: %q. The bridge sends this and "+
			"nothing else, so whatever is missing here is missing from the wake", digest)
	}
	after := st.Inbox("worker")
	if len(after) != 1 || after[0].State != core.MsgStatePending {
		t.Errorf("reading the digest moved the mail to %v. A bridge that reads and then "+
			"fails to write to its session has consumed a delivery nobody saw, which is "+
			"the exact failure the digest travels on the notification to avoid",
			after[0].State)
	}
}
