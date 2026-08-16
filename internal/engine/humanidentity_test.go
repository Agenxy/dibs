package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The human stays the human across a restart.
//
// The identity used to be held only as this daemon run's token, so every
// restart un-personed the operator until they unlocked again. Everything keyed
// off it then went quiet: the board stopped marking their row, and mail
// addressed to them stopped raising a notification. That last one is the path
// that exists BECAUSE the person is not in a loop, so its absence is exactly
// the kind nobody notices.
//
// Caught by deploying the notification work and watching the board fail to mark
// anybody as the human one restart later.
func TestTheHumanIsStillTheHumanAfterARestart(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	led := &memLedger{}
	first := New(st, led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go first.Run(ctx)

	id, _, err := first.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("setup: no human agent was minted")
	}
	if got := first.HumanIdentity(); got != id {
		t.Fatalf("HumanIdentity = %q before any restart, want %q", got, id)
	}

	// A new engine over the SAME state is what a restart looks like from here:
	// the ledger and the board survive, the run's in-memory token does not.
	second := New(st, led, deadProber{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go second.Run(ctx2)

	if got := second.HumanIdentity(); got != id {
		t.Errorf("after a restart HumanIdentity = %q, want %q. The board would stop "+
			"marking the operator's row and mail addressed to them would stop reaching "+
			"them, with nothing saying so", got, id)
	}
}

// Reading the board must still not make anybody a participant. Recovering an
// EXISTING registration is reading what is already there; minting one on a
// board that has never had a human is not.
func TestAnUnusedBoardHasNoHuman(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	if got := e.HumanIdentity(); got != "" {
		t.Errorf("HumanIdentity = %q on a board nobody has unlocked: opening the board "+
			"put somebody on the roster", got)
	}
}

// A person is not a process, and a row that says otherwise must heal itself.
//
// The human's registration used to carry `os.Getpid()`: the DAEMON's pid, which
// is alive at the instant it is written and gone by the next start. The liveness
// sweep then probed a dead process and honestly reported the operator as
// `process gone`, on their own board, forever.
//
// Writing no pid fixes the rows written afterwards and nothing else. The bad pid
// is in the ledger of every board that ran the old build, and the human's
// registration is only rewritten when they ACT, so an operator who reads the
// board and closes it is never repaired.
//
// This registers WITH a pid on purpose, which is what the old code did, and
// requires the repair to clear it. Run it against the commit before the repair
// and it fails on the last check, which is the only reason to trust it.
func TestAPidRecordedAgainstTheHumanIsCleared(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Exactly the op the old build wrote, pid and all.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: humanName(),
		Description: "the human at the board",
		AgentKind:   core.KindPersistent,
		Nonce:       humanNonce(),
		SessionID:   humanNonce(),
		PID:         4242,
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	id, _ := res["agent_id"].(string)
	if id == "" {
		t.Fatal("setup: registering the human returned no id")
	}
	if st.Agents[id].PID != 4242 {
		t.Fatalf("setup: the pid did not land, so this test cannot show it being "+
			"cleared: PID = %d", st.Agents[id].PID)
	}

	e.RepairHumanProcess(ctx)

	if got := st.Agents[id].PID; got != 0 {
		t.Errorf("the human's row still records pid %d after the repair. The liveness "+
			"sweep probes it, finds nothing, and reports the person at the keyboard as "+
			"a dead process on their own board", got)
	}
}

// The repair repairs; it must not recruit.
//
// It runs at every daemon start, and a board whose operator has never acted has
// no human on it deliberately: reading the board makes nobody a participant.
// A repair that registered one would put a permanent row, a mailbox and a
// liveness lease on every board that was ever merely opened.
func TestTheRepairDoesNotMintAHuman(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	e.RepairHumanProcess(ctx)

	if got := e.HumanIdentity(); got != "" {
		t.Errorf("HumanIdentity = %q after the repair ran on an unused board: it "+
			"registered somebody rather than repairing somebody", got)
	}
	if n := len(st.Agents); n != 0 {
		t.Errorf("the board has %d agent(s) after repairing a board with none", n)
	}
}

// Two agents must not be able to promote each other.
//
// core validates WHICH roles may be requested and on what message type. It
// cannot validate the recipient, because core does not know that humans exist,
// and that ignorance is what keeps it a pure state machine. So the recipient
// rule lives in the engine, next to the identity it already owns, and this is
// the test that it is actually there: without it, `a` requests coordinator from
// `b`, `b` approves, and both are coordinators. That is self-promotion with one
// extra participant, and it would defeat the whole reason a role is a human's
// to give.
func TestAnAgentCannotRequestARoleFromAnotherAgent(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	mk := func(name, nonce string) string {
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := res["token"].(string)
		if tok == "" {
			t.Fatal("setup: no token for " + name)
		}
		return tok
	}
	ta := mk("a", "n-a")
	mk("b", "n-b")

	_, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: ta, To: "b", MsgType: core.MsgRequest,
		Body: "promote me", Grant: core.RoleCoordinator,
	})
	if err == nil {
		t.Fatal("an agent sent a role request to another agent: they can now promote " +
			"each other by approving in turn")
	}
	// Refused at ingress, so nothing is written down.
	if m := st.Messages; len(m) != 0 {
		t.Errorf("the refused request was still ledgered as a message: %d present", len(m))
	}
}

// And with no human on the board there is nobody to ask, which must be said
// rather than silently accepted: a board nobody has opened has no `human: true`
// row, and a request addressed at a guess would sit unanswered until it expired.
func TestARoleRequestNeedsAHumanToExist(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "a", AgentKind: core.KindPersistent, Nonce: "n-a",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: tok, To: "a", MsgType: core.MsgRequest,
		Body: "promote me", Grant: core.RoleCoordinator,
	}); err == nil {
		t.Error("a role request was accepted on a board with no human: addressed to " +
			"itself, no less")
	}
}
