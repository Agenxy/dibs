package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A session that states its own id takes it back from an agent that only
// GUESSED it, and takes nothing from one that stated it.
//
// Without this a mis-binding is permanent. The engine infers a session by
// directory when a caller states none: it picks an id announced from that cwd
// recently and assumes the agent registering now is that session. When an agent
// is swept while its session keeps running, the id it held is inherited by the
// next agent in that directory, which then receives its wake notifications.
// mayClaimSession then correctly refuses the rightful session its own id,
// because somebody holds it, and nothing ever notices: on this project's own
// board that ran for hours with one agent's mail announced into another's
// context, and a daemon restart did not clear it, because the binding is in the
// ledger.
//
// So the two are no longer indistinguishable. A guess yields; a claim does not.
func TestAStatedClaimTakesBackAGuessedBinding(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const sid = "19d67315-7718-491e-be3f-3864f577eeed"

	mk := func(name, token string) {
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: name, NewToken: token,
		}, now); err != nil {
			t.Fatalf("setup %s: %v", name, err)
		}
	}
	mk("inheritor", "tok-inheritor")
	mk("rightful", "tok-rightful")

	// The inheritor holds it because the daemon GUESSED, which is what the
	// directory inference does when a caller states nothing.
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpUpdate, Token: "tok-inheritor", Description: "d",
		SessionAlias: sid, SessionGuessed: true,
	}, now); err != nil {
		t.Fatalf("setup: the guessed binding did not apply: %v", err)
	}
	if h := st.AgentBySession(sid); h == nil || h.ID != "inheritor" {
		t.Fatalf("setup: %v holds the id, wanted the inheritor", h)
	}
	if !st.Agents["inheritor"].SessionGuessed {
		t.Fatal("setup: the binding is not recorded as a guess, so the case below " +
			"is not the one intended")
	}

	if !e.mayClaimSession(sid, "tok-rightful") {
		t.Error("the session that states this id was refused it, because an agent " +
			"that merely inherited it holds it. That is the mis-binding being " +
			"permanent: the rightful session never receives its own wakes and the " +
			"holder has no reason to notice it is holding one")
	}

	// AND THE CONVERSE, which is what stops this being a way to steal one. Once
	// an agent has STATED an id, a different agent may not take it.
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpUpdate, Token: "tok-rightful", Description: "d",
		SessionAlias: sid, SessionGuessed: false,
	}, now); err != nil {
		t.Fatalf("the rightful session could not bind: %v", err)
	}
	if st.Agents["rightful"].SessionGuessed {
		t.Fatal("a stated binding was recorded as a guess, which would let the next " +
			"caller take it straight back")
	}
	if e.mayClaimSession(sid, "tok-inheritor") {
		t.Error("an agent took back a session id that its owner had STATED. A guess " +
			"yielding is a repair; a claim yielding is theft, and this rule must " +
			"only do the first")
	}
}

// A historical op carries no such field, so it decodes false and reads as
// STATED. Nothing already on disk starts yielding when this ships.
//
// The conservative direction on purpose: treating old bindings as guesses would
// make every agent on an upgraded board reclaimable by whoever states its id
// first, which is a far worse failure than the one being fixed.
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
	if st.Agents["old"].SessionGuessed {
		t.Fatal("setup: a register that predates the field must decode as stated")
	}
	if e.mayClaimSession(sid, "tok-new") {
		t.Error("a binding written before this field existed was treated as a guess " +
			"and taken away. Every agent on an upgraded board would be reclaimable " +
			"by whoever states its id first")
	}
}
