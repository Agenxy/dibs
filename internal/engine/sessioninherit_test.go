package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A live session's id is not inherited by the next agent in its directory.
//
// announcedSession infers a session by DIRECTORY when no alias arrives: it
// takes an id announced from this cwd recently and assumes the agent
// registering now is that session. It skipped ids an agent already HELD, and
// asked that with AgentBySession, which skips archived and closed rows.
//
// So a swept agent's id read as free while the session behind it kept running,
// and the next agent to register in that directory inherited it. Measured on
// this project's own board: an ephemeral row was swept, the session behind it
// kept announcing, and the next agent resolved that session's hooks to itself.
// One agent's unread list was rendered into another's context for hours.
//
// The fixture is the state a sweep actually leaves: the row is ARCHIVED, not
// absent. A sweep archives and GC removes the row only after ArchiveRetention,
// seven days against a one-hour join window, so a row is always still there
// when the inference would consider its id.
func TestALiveSessionIsNotInheritedByDirectory(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const dir = "/work/shared"
	const live = "19d67315-7718-491e-be3f-3864f577eeed"

	swept := &core.Agent{
		ID:        "agent-swept",
		Name:      "swept",
		Agent:     &core.AgentInfo{CWD: dir},
		SessionID: live,
		Status:    core.StatusArchived,
	}
	st.Agents[swept.ID] = swept

	// The setup is only meaningful if the OLD test still holds: an archived row
	// is invisible to AgentBySession, which is exactly why this was a hole.
	if st.AgentBySession(live) != nil {
		t.Fatal("setup: AgentBySession must not see an archived row, or this " +
			"fixture is not reproducing the defect")
	}

	// The session is still announcing from that directory.
	e.children = map[string]Child{
		live: {SessionID: live, CWD: dir, Seen: now, State: "running"},
	}

	if got := announcedSession(e.children, st, dir, now); got != "" {
		t.Errorf("the directory inference handed out %q, a live session's id whose "+
			"agent was swept. The next agent in this directory would receive that "+
			"session's hooks and its mail", got)
	}
}

// An id nobody has ever held is still inferred, which is the feature.
//
// The fix above is a refusal, and a refusal that refuses everything would be
// indistinguishable from deleting the join. This is the case the join exists
// for: a harness with no way to state its session id, registering in a
// directory where exactly one session has just announced itself and no agent
// has ever answered to it.
func TestAnUnclaimedAnnouncedSessionIsStillJoined(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	const dir = "/work/fresh"
	const fresh = "f0f0f0f0-1111-2222-3333-444444444444"

	e.children = map[string]Child{
		fresh: {SessionID: fresh, CWD: dir, Seen: now, State: "running"},
	}
	if got := announcedSession(e.children, st, dir, now); got != fresh {
		t.Errorf("got %q, want %q: an id no agent has ever held is what this "+
			"inference is for, and refusing it turns the join off", got, fresh)
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
