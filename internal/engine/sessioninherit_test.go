package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A live session's id must not be inherited by the next agent in its directory.
//
// announcedSession infers a session by DIRECTORY when no alias arrives: it
// takes an id announced from this cwd recently and assumes the agent
// registering now is that session. It skips ids an agent already HOLDS, which
// is not the same as ids still in USE.
//
// So when an agent is swept while its session keeps running, the id it held
// becomes unheld and stays live, and the next agent to register in that
// directory inherits it. Measured on this project's own board: an ephemeral row
// was swept, the session behind it kept announcing, and the next agent resolved
// that session's hooks to itself. One agent's unread list was rendered into
// another's context for hours.
//
// The guard asks whether an AGENT holds the id. The question is whether a
// SESSION is alive behind it.
func TestALiveSessionIsStillInheritedByDirectory(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const dir = "/work/shared"
	const live = "19d67315-7718-491e-be3f-3864f577eeed"

	// A session announcing from that directory right now, whose agent is gone.
	e.children = map[string]Child{
		live: {SessionID: live, CWD: dir, Seen: now, State: "running"},
	}
	if st.AgentBySession(live) != nil {
		t.Fatal("setup: no agent must hold this id, which is what makes it look free")
	}

	// ASSERTED, not logged, and asserted in the direction of the DEFECT.
	//
	// This used to skip when the inference stopped firing and otherwise print a
	// line, so it could not fail either way: a test named for a regression that
	// guarded nothing. The review said so.
	//
	// A known-open defect is worth pinning in the shape it actually has, so the
	// day somebody fixes it this FAILS and they delete it on purpose, rather
	// than the fix landing beside a test that quietly kept passing. What is
	// wrong: the join asks whether an AGENT holds a recently announced id, never
	// whether the SESSION behind it is alive and somebody else's, so a swept row
	// leaves a live session's id for the next agent registering in that
	// directory. See CHANGELOG, which discloses it as open.
	got := announcedSession(e.children, st, dir, now)
	if got != live {
		t.Fatalf("the directory inference no longer hands out a live session's id "+
			"(got %q, expected %q).\n"+
			"  If that is because the join now checks whether the SESSION is alive, "+
			"this defect is FIXED: delete this test and the open-defect note in the "+
			"changelog. If it is because the join window or path cleaning changed, "+
			"rewrite the fixture: this must not go back to passing by accident.",
			got, live)
	}
}

// And the caller's OWN id is preferred over that guess, which is what removes
// the exposure for anything behind the stdio bridge.
//
// The bridge sends the session it is running inside on every call, so there is
// nothing to infer. This asserts the ordering: an alias that arrived with the
// call wins, and the directory guess is only consulted when none did.
func TestAnAliasThatArrivedWithTheCallBeatsTheDirectoryGuess(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const dir = "/work/shared"
	const somebodyElse = "19d67315-7718-491e-be3f-3864f577eeed"
	const mine = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"

	e.children = map[string]Child{
		somebodyElse: {SessionID: somebodyElse, CWD: dir, Seen: now, State: "running"},
	}
	// Setup: the guess WOULD hand over the other session, so preferring the
	// supplied id is doing real work here rather than agreeing by luck.
	if announcedSession(e.children, st, dir, now) != somebodyElse {
		t.Fatal("setup: the directory guess does not fire, so this proves nothing")
	}

	// mayClaimSession is what the ingress uses to vet a supplied alias. An id
	// nobody holds is claimable; that is how every agent binds its own.
	if !e.mayClaimSession(mine, "any-token") {
		t.Fatal("an agent cannot claim its own unheld session id, which would stop " +
			"the supplied-id path working at all")
	}
	// And the other session's id is refused once an agent holds it, which is the
	// half that stops a supplied id being used to steal one.
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "holder", NewToken: "tok-holder",
		SessionID: somebodyElse,
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "other", NewToken: "tok-other",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if e.mayClaimSession(somebodyElse, "tok-other") {
		t.Error("a supplied alias naming another agent's session was accepted. " +
			"Preferring the caller's id must not become a way to assert somebody " +
			"else's")
	}
	_ = time.Second
}
