package core

import (
	"testing"
	"time"
)

// The branch that recognises a real session MOVE re-arms the gate, takes the
// process and repoints every binding, while returning the row's existing
// token. So the session the agent had just left kept a working credential:
// the previous holder could still read the mailbox, still act in whatever role
// the row carries, and its next call moved the wake routing back to itself.
// SECURITY.md promises the previous token is revoked on register, reattach and
// resume.
func TestARecoveryIntoANewSessionRevokesThePreviousToken(t *testing.T) {
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-A", Nonce: "n-keep",
		AgentKind: KindPersistent, SessionID: "host-1", SessionAlias: threadA,
		V7Semantics: true,
	}, now)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	old, _ := res["token"].(string)
	if old == "" {
		t.Fatal("setup: no token issued")
	}

	// The same nonce, inside the TTL, from another thread: a real move.
	moved, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-B", Nonce: "n-keep",
		AgentKind: KindPersistent, SessionID: "host-1", SessionAlias: threadB,
		V7Semantics: true,
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	fresh, _ := moved["token"].(string)
	if fresh == old {
		t.Fatalf("the recovery returned the same token %q: the session the agent left keeps a "+
			"working credential, so the previous holder still reads the mailbox and still acts "+
			"in whatever role this row carries", fresh)
	}
	if s.AgentByToken(old) != nil {
		t.Error("the previous token still resolves to the agent after a recovery into another " +
			"session: SECURITY.md promises it is revoked on register, reattach and resume")
	}
	if got := s.AgentByToken(fresh); got == nil || got.ID != id {
		t.Error("the token the recovery returned does not resolve to the agent")
	}
}

// The response-loss retry must NOT rotate: an identical registration arriving
// twice is one call whose answer was lost, and handing back a new credential
// would revoke the one the caller is already using.
func TestAnIdenticalRetryKeepsItsToken(t *testing.T) {
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-A", Nonce: "n-keep",
		AgentKind: KindPersistent, SessionID: "host-1", SessionAlias: threadA,
		V7Semantics: true,
	}, now)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	old, _ := res["token"].(string)

	// The same call again: nothing about the activation has changed.
	again, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-B", Nonce: "n-keep",
		AgentKind: KindPersistent, SessionID: "host-1", SessionAlias: threadA,
		V7Semantics: true,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got, _ := again["token"].(string); got != old {
		t.Errorf("a response-loss retry rotated the token to %q: the caller is already using "+
			"%q and this revokes it out from under them", got, old)
	}
}
