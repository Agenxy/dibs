package core

import (
	"testing"
	"time"
)

// The restoration reached only the DORMANT recovery branch. An agent that
// comes back and keeps working is active, so a later same-nonce register takes
// the LIVE resume instead, which never put the nonce back: AgentIdentity stays
// empty and the role dibs.toml grants can never reconcile onto it, for as long
// as the agent keeps working, which is indefinitely.
func TestALiveResumePutsARecoveredAgentsNonceBack(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "admin", NewToken: "tok-1", Nonce: "n-keepme",
	}, now)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	id, _ := res["agent_id"].(string)

	// Archive it, which blanks the nonce FIELD and keeps the index.
	stale := now.Add(s.Limits.AgentTTL + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleAgents: []string{id}}, stale); err != nil {
		t.Fatalf("setup sweep to stale: %v", err)
	}
	archived := stale.Add(s.Limits.StaleGrace + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpSweep}, archived); err != nil {
		t.Fatalf("setup sweep to archived: %v", err)
	}

	// Recovered by a HISTORICAL op, which correctly does not restore: this is
	// the v0.0.6 ledger replaying, and it must keep its own semantics.
	back := archived.Add(time.Minute)
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "admin", NewToken: "tok-2", Nonce: "n-keepme",
	}, back); err != nil {
		t.Fatalf("setup historical recovery: %v", err)
	}
	// The premise, asserted rather than assumed: an ACTIVE row carrying no
	// nonce, with the index still resolving it.
	if got := s.Agents[id].Status; got != StatusActive {
		t.Fatalf("setup: the recovered agent is %s, not active, so the live path is not under test", got)
	}
	if s.Agents[id].Nonce != "" {
		t.Fatal("setup: the recovered row already carries a nonce, so there is nothing to restore")
	}
	if s.Nonces["n-keepme"] != id {
		t.Fatal("setup: the nonce index no longer resolves the row")
	}

	// A registration this version writes, while the agent is still live.
	before := s.Serial
	soon := back.Add(time.Minute)
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "admin", NewToken: "tok-3", Nonce: "n-keepme",
		RestoreNonce: true, V7Semantics: true,
	}, soon); err != nil {
		t.Fatalf("live resume: %v", err)
	}
	if got := s.Agents[id].Nonce; got != "n-keepme" {
		t.Errorf("nonce = %q after a live resume, want it restored: the agent has no durable "+
			"identity, so AgentIdentity stays empty and a declared role can never reconcile "+
			"onto it while it keeps working", got)
	}
	// Restoring is replayable state, so the op must be ledgered. An op that
	// changes state without advancing the serial is the invariant this
	// repository guards hardest.
	if s.Serial == before {
		t.Errorf("the serial did not advance (%d) while the nonce was restored: the engine "+
			"ledgers exactly when the serial moves, so this change would be lost on replay", before)
	}
}

// And the historical op is still historical: no RestoreNonce, no restoration,
// on the live path as much as the dormant one.
func TestALiveResumeWithoutTheRecordedDecisionRestoresNothing(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "admin", NewToken: "tok-1", Nonce: "n-keepme",
	}, now)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	s.Agents[id].Nonce = "" // the state a v0.0.6 archive-and-recovery leaves

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "admin", NewToken: "tok-2", Nonce: "n-keepme",
		V7Semantics: true, // but NOT RestoreNonce: an op written before the field
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("live resume: %v", err)
	}
	if got := s.Agents[id].Nonce; got != "" {
		t.Errorf("nonce = %q: an op that recorded no decision restored one anyway, so one "+
			"ledger reconstructs two different boards depending on which binary reads it", got)
	}
}
