package core

import (
	"errors"
	"testing"
)

// An agent registered with neither a nonce nor a session id can never be
// reattached: both recovery paths key on one of those. Its mailbox then keeps
// accepting mail nobody can read, which is not hypothetical. It happened on
// this project's own board, where six messages sat behind an identity nobody
// could become, and the hint printed at that moment named `merge_spaces`, which
// takes SPACE ids and would have failed with E_NO_SPACE.
func TestAnAbandonedMailboxCanBeRecovered(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	lost := reg(t, s, "lost", "tok-lost", t0)
	sender := reg(t, s, "sender", "tok-send", t0)
	heir := reg(t, s, "heir", "tok-heir", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: sender.Token}, t0)
	for _, body := range []string{"first", "second"} {
		mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: sender.Token, To: lost.ID,
			MsgType: MsgNotify, Body: body,
		}, t0)
	}
	s.Agents[lost.ID].Status = StatusDormant
	if n := len(s.Inbox(lost.ID)); n != 2 {
		t.Fatalf("setup: the abandoned agent holds %d message(s), want 2", n)
	}

	res := mustApply(t, s, &Op{
		Kind: OpAdoptAgent, Token: heir.Token, To: lost.ID, AdoptAuthorised: true,
	}, t0)

	if res["messages"] != 2 {
		t.Errorf("moved %v message(s), want 2", res["messages"])
	}
	if n := len(s.Inbox(heir.ID)); n != 2 {
		t.Errorf("the heir holds %d message(s), want 2", n)
	}
	if n := len(s.Inbox(lost.ID)); n != 0 {
		t.Errorf("the abandoned mailbox still holds %d: mail cannot be in two inboxes", n)
	}
	// The record survives. The ledger refers to it, and a board that erased
	// where six messages came from would be lying about their origin.
	if s.Agents[lost.ID] == nil {
		t.Error("the source agent was destroyed; only its mail should have moved")
	}
}

// Taking another agent's mail is the one thing Dibs must never allow, so the
// authorisation is decided outside the fold and recorded. An op that reaches
// Apply without it is refused, whatever it claims about itself.
func TestAdoptingWithoutAuthorisationIsRefused(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	victim := reg(t, s, "victim", "tok-victim", t0)
	thief := reg(t, s, "thief", "tok-thief", t0)
	s.Agents[victim.ID].Status = StatusDormant

	_, _, err := s.Apply(&Op{Kind: OpAdoptAgent, Token: thief.Token, To: victim.ID}, t0)
	if err == nil {
		t.Fatal("an unauthorised adopt was allowed: any agent could take another's mail")
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
		t.Errorf("err = %v, want E_NOT_PERMITTED", err)
	}
	if ce != nil && ce.Hint == "" {
		t.Error("no hint: every error carries the corrective call")
	}
}

// "Abandoned" is a state, not an opinion. An active agent is reading its own
// mail, and moving it would be theft dressed as recovery.
func TestAnActiveAgentsMailIsNotAbandoned(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	live := reg(t, s, "live", "tok-live", t0)
	other := reg(t, s, "other", "tok-other", t0)
	if s.Agents[live.ID].Status != StatusActive {
		t.Fatalf("setup: the agent is %q, not active", s.Agents[live.ID].Status)
	}

	_, _, err := s.Apply(&Op{
		Kind: OpAdoptAgent, Token: other.Token, To: live.ID, AdoptAuthorised: true,
	}, t0)
	if err == nil {
		t.Fatal("an ACTIVE agent's mail was moved out from under it")
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "E_AGENT_ACTIVE" {
		t.Errorf("err = %v, want E_AGENT_ACTIVE", err)
	}
}
