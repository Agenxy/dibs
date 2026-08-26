package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// An INFERRED binding must be recorded as a guess by the production path.
//
// This is the test that was missing, and its absence let a broken repair ship.
// The first version constructed SessionGuessed by hand and asserted on the
// decision downstream of it, so it passed while NOTHING in the engine ever set
// the flag: every inferred binding was recorded as stated, the rightful session
// was refused its own id exactly as before, and a changelog entry said the
// opposite. A test that builds the input the production code was supposed to
// build cannot see that the production code does not build it.
//
// So this drives the real ingress: a session announces from a directory, an
// agent registers there naming no session of its own, and the engine infers.
func TestAnInferredBindingIsRecordedAsAGuessByTheEngine(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const dir = "/work/shared"
	const announced = "19d67315-7718-491e-be3f-3864f577eeed"
	// A session announcing from that directory, which is what the inference
	// keys on. Through the hook path, as a harness would.
	if _, err := e.HookPoll(ctx, announced, "SessionStart", dir, false, false); err != nil {
		t.Fatal("setup:", err)
	}

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "inheritor", Nonce: "n-inheritor",
		AgentKind: core.KindPersistent,
		Agent:     &core.AgentInfo{CWD: dir},
	}); err != nil {
		t.Fatal("register:", err)
	}
	l := st.Agents["inheritor"]
	// Setup must hold: the inference has to have fired, or there is no binding
	// whose provenance could be wrong.
	if !l.HoldsSessionForTest(announced) {
		t.Skipf("the directory inference did not bind %s (join window or path "+
			"cleaning changed); this case needs rewriting rather than passing", announced)
	}

	if !l.GuessedSession(announced) {
		t.Error("a binding the DAEMON inferred was recorded as though the caller " +
			"stated it. The reclaim rule then refuses the session that owns this id, " +
			"which is the repair not working at all while its own test passes")
	}

	// And the consequence the provenance exists for: the rightful session, which
	// states this id, takes it back.
	other, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "rightful", Nonce: "n-rightful", AgentKind: core.KindPersistent,
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := other["token"].(string)
	if !e.mayClaimSession(announced, tok) {
		t.Error("the session that states this id cannot reclaim it from an agent " +
			"that only inherited it")
	}
}

// Provenance is per BINDING, not per agent.
//
// An agent holds a primary and any number of aliases. With one flag per agent,
// whichever binding happened last decided the answer for all of them: adding a
// single guessed alias made a STATED primary claimable by anyone, and a later
// stated alias made an earlier guess permanently non-yielding. Authorisation
// asks about one specific id, so provenance has to be recorded against that id.
//
// Found by the pre-release review, which also noted that fixing the missing
// stamp WITHOUT this would have turned a dormant bug into a live one.
func TestGuessProvenanceIsPerBindingNotPerAgent(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const stated = "11111111-1111-4111-8111-111111111111"
	const guessed = "22222222-2222-4222-8222-222222222222"

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "holder", NewToken: "tok-holder",
		SessionID: stated, SessionGuessed: false,
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	// A second binding on the same agent, this one inferred.
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpUpdate, Token: "tok-holder", Description: "d",
		SessionAlias: guessed, SessionGuessed: true,
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "stranger", NewToken: "tok-stranger",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}

	if !e.mayClaimSession(guessed, "tok-stranger") {
		t.Error("the inferred alias is not reclaimable, so a mis-binding on an " +
			"agent that also holds a stated id can never be repaired")
	}
	if e.mayClaimSession(stated, "tok-stranger") {
		t.Error("the agent's STATED primary became claimable because it later " +
			"acquired a guessed alias. One flag per agent means the last binding " +
			"decides the answer for all of them, and this is that hole")
	}
}

// A historical op carries no such field, decodes false, and reads as STATED.
// Nothing already on disk starts yielding when this ships.
func TestAHistoricalBindingIsTreatedAsStated(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const sid = "19d67315-7718-491e-be3f-3864f577eeed"

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "old", NewToken: "tok-old", SessionID: sid,
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "new", NewToken: "tok-new",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if e.mayClaimSession(sid, "tok-new") {
		t.Error("a binding written before this field existed was treated as a guess " +
			"and taken away. Every agent on an upgraded board would be reclaimable " +
			"by whoever states its id first")
	}
}

// The previous occupant's mail must be unreadable, not merely unlisted.
//
// A new agent taking a reused name is given a watermark past the mail addressed
// to whoever held the id before. Inbox honoured it and read_mail did not,
// authorising on the reused id alone: so the replacement could not SEE that
// mail and could still fetch its body by serial, which is the entirety of what
// the watermark protects. The enumeration half shipped with a changelog entry
// claiming the privacy. Found by the pre-release review.
//
// Both directions are asserted, because outbound mail the predecessor SENT is
// readable by the same route and is somebody else's conversation too.
func TestAReplacementCannotReadThePredecessorsMail(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})

	// EVERY MUTATION BEFORE THE LOOP STARTS. The first version of this mixed
	// direct Apply from the test goroutine with e.Do while e.Run was going,
	// which is the single-writer violation this package has a production fix
	// for. It passed three runs in five. Setup happens here, on one goroutine,
	// and only the reads below go through the loop.
	must := func(op *core.Op) core.Result {
		t.Helper()
		r, _, err := st.Apply(op, t0Engine())
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	must(&core.Op{Kind: core.OpRegister, Name: "peer", NewToken: "tok-peer"})
	must(&core.Op{Kind: core.OpRegister, Name: "seat", NewToken: "tok-seat"})
	inbound, _ := must(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-peer", To: "seat",
		MsgType: core.MsgNotify, Body: "INBOUND SECRET",
	})["msg_serial"].(uint64)
	outbound, _ := must(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-seat", To: "peer",
		MsgType: core.MsgNotify, Body: "OUTBOUND SECRET",
	})["msg_serial"].(uint64)

	// The row vanishes, as a pre-v0.0.7 sweep leaves it, and the name returns.
	delete(st.Agents, "seat")
	res := must(&core.Op{
		Kind: core.OpRegister, Name: "seat", NewToken: "tok-seat2", V7Semantics: true,
	})
	tok, _ := res["token"].(string)
	if tok == "" {
		tok = "tok-seat2"
	}
	if st.Agents["seat"].TruncatedBefore == 0 {
		t.Fatal("setup: the replacement has no watermark, so this asserts nothing")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	for name, serial := range map[string]uint64{"inbound": inbound, "outbound": outbound} {
		// ASSERTED ON THE KEY THE CALL ACTUALLY RETURNS. The first version
		// looked for "body", which GetMessage never sets: it returns the whole
		// message under "message". So the check passed against a commit that
		// hands the mail over, which is the vacuous-test failure this cycle
		// keeps producing. Verified by printing the result rather than
		// reasoning about it.
		got, err := e.GetMessage(ctx, tok, serial)
		if err == nil && got["message"] != nil {
			t.Errorf("the replacement read the previous occupant's %s mail by "+
				"serial (result %v). Not listing it is not protecting it", name, got)
		}
	}
}

// An agent that comes BACK still reads its own history.
//
// The rule that hides a predecessor's mail is "older than this agent", so it
// must not fire for an agent reattaching to its own row: a reattach keeps its
// original CreatedSerial, and its mail is not older than itself. Without this
// the privacy fix would silently erase every returning agent's history, which
// is a worse failure than the one it repairs.
func TestAReattachingAgentStillReadsItsOwnMail(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	must := func(op *core.Op) core.Result {
		t.Helper()
		r, _, err := st.Apply(op, t0Engine())
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	must(&core.Op{Kind: core.OpRegister, Name: "peer", NewToken: "tok-peer"})
	must(&core.Op{
		Kind: core.OpRegister, Name: "comeback", NewToken: "tok-1",
		Nonce: "n-comeback", AgentKind: core.KindPersistent,
	})
	ser, _ := must(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-peer", To: "comeback",
		MsgType: core.MsgNotify, Body: "yours",
	})["msg_serial"].(uint64)

	// The same agent returns with its nonce, which is a reattach and not a new
	// occupant. USE THE TOKEN IT HANDS BACK: a same-nonce register inside the
	// TTL short-circuits and returns the ORIGINAL result without applying, so
	// the token supplied here is not necessarily the one that is live. The first
	// version of this test assumed it rotated and failed on E_BAD_TOKEN.
	back := must(&core.Op{
		Kind: core.OpRegister, Name: "comeback", NewToken: "tok-2",
		Nonce: "n-comeback", AgentKind: core.KindPersistent, V7Semantics: true,
	})
	tok, _ := back["token"].(string)
	if tok == "" {
		t.Fatal("setup: the reattach returned no token")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	got, err := e.GetMessage(ctx, tok, ser)
	if err != nil || got["message"] == nil {
		t.Errorf("an agent that reattached to its own row can no longer read its "+
			"own mail (err %v, result %v). The rule is older-than-this-AGENT, and a "+
			"reattach is the same agent", err, got)
	}
}
