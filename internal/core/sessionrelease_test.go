package core

import "testing"

// An agent can give up a session binding that is not its session's.
//
// This is the only exit from a state that was otherwise permanent. When an id
// is bound to the wrong agent, the board wakes that agent rather than the
// mailbox's owner; the owner is refused its own id, correctly, because somebody
// holds it; and the holder has no reason to notice. Recording whether a binding
// was stated or guessed stops NEW ones going wrong and cannot help the ones
// already on disk, which decode as stated on purpose. Measured on this
// project's own board, where a daemon restart replayed the mis-binding exactly
// and left the owner with no move to make.
//
// Only ever the CALLER's own, which is what makes it safe with no role
// attached: an agent giving up its own bindings can strand nothing but itself.
func TestAnAgentCanReleaseItsOwnSessionBinding(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "holder", "tok", t0)
	l := s.Agents["holder"]
	l.SessionID = "19d67315-7718-491e-be3f-3864f577eeed"
	l.SessionAliases = []string{"host-5360"}
	l.GuessedSessions = []string{"19d67315-7718-491e-be3f-3864f577eeed"}

	// Setup must hold, or the release below proves nothing.
	if s.AgentBySession("host-5360") == nil {
		t.Fatal("setup: the alias does not resolve, so releasing it changes nothing")
	}

	res := mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok", Description: "d", ReleaseSession: true,
	}, t0)

	if got := s.Agents["holder"].SessionID; got != "" {
		t.Errorf("the primary session id survived the release: %q", got)
	}
	if got := s.Agents["holder"].GuessedSessions; len(got) != 0 {
		t.Errorf("released bindings left their provenance behind: %v. A later "+
			"binding of the same id would inherit a verdict from an agent that no "+
			"longer holds it", got)
	}
	if got := s.Agents["holder"].SessionAliases; len(got) != 0 {
		t.Errorf("aliases survived the release: %v. Each one is still a live wake "+
			"target pointing at the wrong agent", got)
	}
	if s.AgentBySession("19d67315-7718-491e-be3f-3864f577eeed") != nil {
		t.Error("the released id still resolves to this agent, so the session it " +
			"belongs to still cannot claim it back")
	}
	if res["session_released"] != true {
		t.Errorf("the release is not reported (%v), so a caller cannot tell it "+
			"happened from a call that quietly did nothing", res["session_released"])
	}
}

// And an ordinary update leaves the binding alone, or every agent would lose
// its wake path on the call it makes to change its own description.
func TestAnOrdinaryUpdateKeepsTheSessionBinding(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "keeper", "tok", t0)
	s.Agents["keeper"].SessionID = "19d67315-7718-491e-be3f-3864f577eeed"

	mustApply(t, s, &Op{Kind: OpUpdate, Token: "tok", Description: "just a description"}, t0)

	if got := s.Agents["keeper"].SessionID; got == "" {
		t.Error("an ordinary update cleared the session binding, so any agent that " +
			"revises its description stops being wakeable")
	}
}

// Stating an id you already hold must clear the guess against it.
//
// bindHarnessSession reports "" when there is nothing NEW to bind, which is
// exactly the case when a session names an id the agent already carries. The
// provenance update sat behind that early return, so an agent that explicitly
// confirmed its own session stayed marked as having only inherited it, and
// remained reclaimable by any other authenticated agent. The binding was right;
// the claim about where it came from was not. Found by the pre-release review.
func TestReassertingAnInferredSessionMakesItStated(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "owner", "tok", t0)
	const sid = "19d67315-7718-491e-be3f-3864f577eeed"

	// Inferred first, which is how the daemon binds when a caller states none.
	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok", Description: "d",
		SessionAlias: sid, SessionGuessed: true,
	}, t0)
	if !s.Agents["owner"].GuessedSession(sid) {
		t.Fatal("setup: the binding is not recorded as a guess, so there is nothing " +
			"for the reassertion below to upgrade")
	}

	// Now the session says so itself. The binding does not change; its
	// provenance must.
	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok", Description: "d",
		SessionAlias: sid, SessionGuessed: false,
	}, t0)

	if s.Agents["owner"].GuessedSession(sid) {
		t.Error("an agent that stated its own session id is still marked as having " +
			"inherited it, so any other authenticated agent can take it away and " +
			"redirect this one's wakes")
	}
	if !s.Agents["owner"].HoldsSessionForTest(sid) {
		t.Error("the reassertion lost the binding it was confirming")
	}
}
