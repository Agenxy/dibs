package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A session id already held by another agent must not be claimable, and this
// had no test at all.
//
// mayClaimSession is the vetting behind "Vetted, not trusted" in exec: it is
// what stops one agent naming another's session id and taking over its wake
// delivery. Three agents on this board spent a night concluding, from the
// outside, that the field was caller-controlled and unguarded, and one of them
// proved it against the live daemon by binding an invented string. What that
// actually proved is the UNCLAIMED case, which is allowed by design. The
// dangerous case was already refused and nothing anywhere asserted it, so the
// guarantee was one careless edit away from vanishing silently.
//
// Written after removing a duplicate of this check that I had added one layer
// out, before finding this one. Two places answering "may this agent hold this
// session id" is how they come to disagree.
func TestASessionIdHeldByAnotherAgentCannotBeClaimed(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()

	mk := func(name, token, sid string) {
		op := &core.Op{
			Kind: core.OpRegister, Name: name, NewToken: token,
			AgentKind: core.KindPersistent, Nonce: "n-" + name,
		}
		if sid != "" {
			op.SessionID = sid
		}
		if _, _, err := st.Apply(op, now); err != nil {
			t.Fatalf("setup %s: %v", name, err)
		}
	}
	const sid = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	mk("owner", "tok-owner", sid)
	mk("stranger", "tok-stranger", "")

	if !e.mayClaimSession(sid, "tok-owner") {
		t.Error("the agent that already holds this session id may not re-assert it. " +
			"Every check_in re-sends it, so this would stop the owner binding on " +
			"the call it makes most")
	}
	if e.mayClaimSession(sid, "tok-stranger") {
		t.Error("a DIFFERENT agent claimed a session id that is already held. That " +
			"redirects the holder's wake notifications into the claimant's session " +
			"and silently stops the holder being woken: last writer wins, and " +
			"nothing tells the loser")
	}
	if !e.mayClaimSession("nobody-holds-this-one", "tok-stranger") {
		t.Error("an unclaimed session id was refused, which would stop an ordinary " +
			"agent ever binding its own")
	}
}

// The inheritance path, which is the one that actually bit this board.
//
// announcedSession infers a session id from the children map by DIRECTORY when
// the caller supplied none. It correctly skips an id that an agent already
// holds. What it cannot see is whether the SESSION behind that id is still
// alive and belongs to somebody else.
//
// So: a session announces its id from a directory, the agent holding that id is
// swept, and the id is now unheld while the session is still running and still
// announcing. The next agent to check in from the same directory inherits a
// LIVE session's id. Measured on this board: my own row was swept, my session
// kept announcing, and the next agent to check in from that directory ended up
// resolving my hooks to itself. Every wake for me then landed in its context.
func TestAnUnheldButStillLiveSessionIdIsStillInherited(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const dir = "/work/shared"
	const liveSession = "19d67315-7718-491e-be3f-3864f577eeed"

	// A session announcing from that directory, recently, whose agent is gone.
	e.children = map[string]Child{
		liveSession: {SessionID: liveSession, CWD: dir, Seen: now, State: "running"},
	}
	if st.AgentBySession(liveSession) != nil {
		t.Fatal("setup: no agent should hold this id, which is the whole point")
	}

	got := announcedSession(e.children, st, dir, now)
	if got != liveSession {
		t.Skipf("inference did not fire (%q); the join window or cwd cleaning has "+
			"changed and this case needs rewriting rather than silently passing", got)
	}
	// Documented rather than asserted as correct: this IS the current behaviour
	// and it is how a live session's id changes hands. Recorded so the next
	// person does not have to re-derive it from a ledger at three in the
	// morning, as three agents did.
	t.Logf("an unheld but still-live session id (%s) is inherited by whoever "+
		"checks in from %s next: the guard asks whether an AGENT holds it, never "+
		"whether the SESSION is alive and somebody else's", liveSession, dir)
}
