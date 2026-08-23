package engine

import (
	"context"
	"testing"
	"time"

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
		// The DAEMON's own op, which is what this reproduces. A caller sending
		// the same thing is refused now; see TestAnAgentCannotRegisterAsTheHuman.
		HumanMint: true,
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

// A grant cannot be approved by whoever the mailbox ended up with.
//
// The send-time check ("addressed to the human") is not enough on its own, and
// the gap took an independent review to see. Adoption rewrites the `to` of every
// message in a mailbox, and a coordinator may adopt, so a pending request to a
// dormant human could be inherited and approved by an agent. A person's row is
// dormant whenever they are not typing, so step two needs no arranging.
//
// Two rules now close it, and this exercises both ends: the human's mailbox is
// not adoptable at all, and approving a grant re-checks the human at APPROVAL
// time rather than trusting a recipient that can be rewritten.
func TestARoleCannotBeGrantedByInheritingTheHumansMailbox(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// A human, and a coordinator who is not them.
	humanID, humanTok, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	_ = humanTok
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "boss", AgentKind: core.KindPersistent, Nonce: "n-boss",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	bossTok, _ := res["token"].(string)
	st.Agents["boss"].Role = core.RoleCoordinator

	// DORMANT, which is the whole point and is not an unusual state to arrange:
	// a person's row goes quiet whenever they are not at the keyboard, because
	// silence is their entire liveness model. Without this the test passes on a
	// board with the bug in it, since adoption already refuses an ACTIVE source
	// and the human had just registered.
	st.Agents[humanID].Status = core.StatusDormant

	// The coordinator tries to take the human's mailbox, which is the move that
	// made the escalation reachable.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpAdoptAgent, Token: bossTok, To: humanID,
	}); err == nil {
		t.Error("a coordinator adopted the HUMAN's mailbox. core/roles.go says the " +
			"role gets 'no power to read another agent's mail. Breadth, not " +
			"intrusion', and this collects every private message the operator ever " +
			"received, plus any request awaiting their approval")
	}
}

// And the approval-time check stands on its own, so the fix does not rest
// entirely on adoption being blocked.
func TestOnlyTheHumanCanApproveARoleGrant(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	humanID, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	mk := func(name, nonce string) string {
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	askerTok := mk("asker", "n-a")
	bossTok := mk("boss", "n-b")
	st.Agents["boss"].Role = core.RoleCoordinator

	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: askerTok, To: humanID,
		MsgType: core.MsgRequest, Body: "promote me", Grant: core.RoleCoordinator,
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	serial, _ := sent["msg_serial"].(uint64)

	// Simulate the mailbox having been inherited, which is what adoption does.
	st.Messages[serial].To = "boss"

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: bossTok, MsgSerial: serial, Disposition: "approve",
	}); err == nil {
		t.Error("a coordinator approved a role grant addressed to the human. " +
			"'Addressed to the human' has to be true when somebody says yes, not " +
			"only when somebody asked")
	}
	if st.Agents["asker"].IsCoordinator() {
		t.Error("the asker was promoted with no human anywhere in the story")
	}
}

// A person gets a person's deadline.
//
// The default is ten minutes: right for an agent in a loop, absurd for somebody
// who answers when they next look at their machine. Measured on this board, on
// the request that would have made the maintainer a coordinator: sent,
// delivered, never seen because a Focus mode swallowed the notification, and
// expired thirty minutes later as `expired_recipient_dormant` while the
// operator was away. The feature worked; the clock was set for somebody else.
func TestARequestToTheHumanOutlivesTheDefaultDeadline(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	humanID, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	r, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "asker", AgentKind: core.KindPersistent, Nonce: "n-a",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := r["token"].(string)

	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: tok, To: humanID,
		MsgType: core.MsgRequest, Body: "please approve",
	})
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := sent["msg_serial"].(uint64)
	m := st.Messages[serial]
	if m == nil {
		t.Fatal("setup: the message was not stored")
	}
	got := m.Deadline.Sub(m.SentAt)
	if got <= core.DefaultLimits().DefaultDeadline {
		t.Errorf("a request to the human expires after %s, the same as one to an "+
			"agent. A person is not in a loop, which is the premise the product "+
			"rests on, and this default contradicts it", got)
	}

	// An agent recipient keeps the short default: this must not become "every
	// deadline is a day", which would leave stale questions sitting on every
	// board for a day apiece.
	sent2, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: tok, To: "asker",
		MsgType: core.MsgRequest, Body: "self",
	})
	if err == nil {
		if m2 := st.Messages[sent2["msg_serial"].(uint64)]; m2 != nil {
			if m2.Deadline.Sub(m2.SentAt) > core.DefaultLimits().DefaultDeadline {
				t.Error("an agent-to-agent request also got the human's deadline")
			}
		}
	}

	// And an explicit deadline still wins, in both directions.
	sent3, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: tok, To: humanID,
		MsgType: core.MsgRequest, Body: "quick", DeadlineSec: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m3 := st.Messages[sent3["msg_serial"].(uint64)]; m3 != nil {
		if d := m3.Deadline.Sub(m3.SentAt); d > 5*time.Minute {
			t.Errorf("an explicit 60s deadline became %s: the sender's word is still "+
				"the sender's", d)
		}
	}
}

// The human's mailbox cannot be taken by approving a request for it either.
//
// guardHumanMailbox covered the direct adopt_agent call and nothing else, so an
// agent could send a request carrying `adopt: <the human>`, a coordinator could
// approve it in good faith, and the operator's whole mailbox moved to the
// asker. Found by an independent reviewer.
//
// Second escalation in one release from guarding a door rather than the effect
// behind it, which is the lesson worth keeping.
func TestApprovingCannotAdoptTheHumansMailbox(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	humanID, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	mk := func(name, nonce string) string {
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	attacker, boss := mk("attacker", "n-a"), mk("boss", "n-b")
	st.Agents["boss"].Role = core.RoleCoordinator
	st.Agents[humanID].Status = core.StatusDormant

	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: attacker, To: "boss",
		MsgType: core.MsgRequest, Body: "that mailbox is mine", Adopt: humanID,
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: boss, MsgSerial: sent["msg_serial"].(uint64),
		Disposition: "approve",
	}); err == nil {
		t.Error("a coordinator approved a request that moved the OPERATOR's entire " +
			"mailbox to the agent that asked for it")
	}
}

// And an ARCHIVED human still owns their mailbox, at both doors.
//
// humanIdentityLocked answers "who may act as the human" and correctly returns
// nothing once that row is archived. Both mailbox guards read the same answer
// and therefore treated an archived human as no human at all, which is fail-open
// on the one question where ownership, not authority, is what matters.
//
// The state arrives on its own: thirty days dormant archives the human, and the
// row and its mail survive the seven-day retention window after that. Core
// rejects only an ACTIVE source as unabandoned, so for that week the operator's
// private mailbox was adoptable by a coordinator, directly or by approving a
// peer's request for it. Whose mail it is does not change when they stop typing.
//
// The two tests above use a DORMANT human, which is the state that was already
// covered and the one branch this hole is not in.
func TestAnArchivedHumansMailboxIsStillTheirs(t *testing.T) {
	for _, door := range []string{"adopt_agent", "approving a request"} {
		t.Run(door, func(t *testing.T) {
			st := core.NewState("test", core.DefaultLimits())
			e := New(st, &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go e.Run(ctx)

			humanID, _, err := e.HumanAgent(ctx)
			if err != nil {
				t.Fatal("setup:", err)
			}
			mk := func(name, nonce string) string {
				r, err := e.Do(ctx, &core.Op{
					Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
				})
				if err != nil {
					t.Fatal("setup:", err)
				}
				tok, _ := r["token"].(string)
				return tok
			}
			attacker, boss := mk("attacker", "n-a"), mk("boss", "n-b")
			st.Agents["boss"].Role = core.RoleCoordinator

			// SOMETHING TO TAKE. Adoption refuses an empty mailbox, so without
			// this both doors answer "nothing to adopt" and the test passes on a
			// board with the hole wide open.
			if _, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: attacker, To: humanID,
				MsgType: core.MsgNotify, Body: "a private note for the operator",
			}); err != nil {
				t.Fatal("setup:", err)
			}

			// The state the sweep produces after thirty dormant days. The row
			// and its mail are still here for the retention week.
			st.Agents[humanID].Status = core.StatusArchived
			if got := e.HumanIdentity(); got != "" {
				t.Fatalf("setup: an archived human still resolves as the acting "+
					"identity (%q), so this test is not in the state it names", got)
			}

			var err2 error
			if door == "adopt_agent" {
				_, err2 = e.Do(ctx, &core.Op{
					Kind: core.OpAdoptAgent, Token: boss, To: humanID,
				})
			} else {
				sent, serr := e.Do(ctx, &core.Op{
					Kind: core.OpSendMessage, Token: attacker, To: "boss",
					MsgType: core.MsgRequest, Body: "that mailbox is mine", Adopt: humanID,
				})
				if serr != nil {
					t.Fatal("setup:", serr)
				}
				_, err2 = e.Do(ctx, &core.Op{
					Kind: core.OpRespond, Token: boss, MsgSerial: sent["msg_serial"].(uint64),
					Disposition: "approve",
				})
			}
			if err2 == nil {
				t.Errorf("%s moved an ARCHIVED operator's entire mailbox to an agent. "+
					"Archiving is what happens to a human who has not typed for a "+
					"month; their mail is still theirs, and it is still there to take",
					door)
			}
		})
	}
}
