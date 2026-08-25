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
