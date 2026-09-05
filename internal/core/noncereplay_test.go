package core

import (
	"testing"
	"time"
)

// A register op written before v0.0.7 must replay to the board v0.0.6 built.
//
// Restoring a recovered agent's nonce is a correct change and a retroactive
// one, which is the whole difficulty. `Apply` is the fold, and the fold runs
// over ops accepted by OLDER code: v0.0.6 did not put the nonce back, so the
// moment this branch started doing it unconditionally, every register op
// already on disk meant something different depending on which binary read it.
// One ledger, two boards, and `state == fold(ledger)` false across an upgrade.
//
// That is the class AGENTS.md names first and this repository has now shipped
// twice: `lane_kind` -> `agent_kind` silently demoted every persistent agent,
// and the purge sweep made a v0.0.6 ledger refuse to replay at all. The fix is
// the same each time and it is not "be careful": record the decision IN THE OP,
// so replay applies the decision that was actually made rather than the one
// today's code would make.
//
// So this test is written from the ledger's point of view. It does not ask
// whether restoring the nonce is a good idea; it asks whether an op that
// predates the field still means what it meant when it was written.
func TestAPreV007RegisterReplaysWithoutRestoringTheNonce(t *testing.T) {
	// archiveAgent drives a fresh state to the one situation this branch is
	// about: the agent is archived, so its Nonce FIELD is blank while the
	// s.Nonces index that register looks in is still there.
	archiveAgent := func(t *testing.T) (*State, string, time.Time) {
		t.Helper()
		s := NewState("test", DefaultLimits())
		now := time.Unix(1700000000, 0)
		res, _, err := s.Apply(&Op{
			Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keepme",
		}, now)
		if err != nil {
			t.Fatalf("setup register: %v", err)
		}
		id, _ := res["agent_id"].(string)
		if id == "" {
			t.Fatalf("setup: no agent_id in %v", res)
		}
		stale := now.Add(s.Limits.AgentTTL + time.Minute)
		if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleAgents: []string{id}}, stale); err != nil {
			t.Fatalf("setup sweep to stale: %v", err)
		}
		archived := stale.Add(s.Limits.StaleGrace + time.Minute)
		if _, _, err := s.Apply(&Op{Kind: OpSweep}, archived); err != nil {
			t.Fatalf("setup sweep to archived: %v", err)
		}
		// Assert the premise. Without this the whole test passes against an
		// agent that was never archived, where nothing restores anything and
		// every assertion below is vacuously true. Three false alarms in this
		// repository came from a probe whose setup nobody checked.
		if got := s.Agents[id].Status; got != StatusArchived {
			t.Fatalf("setup: agent is %s, not archived", got)
		}
		if s.Agents[id].Nonce != "" {
			t.Fatalf("setup: archiving did not blank the nonce field, so this "+
				"test is not exercising the recovery branch at all (nonce=%q)",
				s.Agents[id].Nonce)
		}
		if s.Nonces["n-keepme"] != id {
			t.Fatalf("setup: the nonce index is gone, so register cannot find " +
				"the row and the recovery branch is never reached")
		}
		return s, id, archived.Add(time.Minute)
	}

	// THE HISTORICAL OP. No RestoreNonce field, exactly as every register
	// already on disk decodes: absent in JSON means the zero value.
	t.Run("without the recorded decision, as v0.0.6 wrote it", func(t *testing.T) {
		s, id, now := archiveAgent(t)
		before := len(s.Agents)
		if _, _, err := s.Apply(&Op{
			Kind: OpRegister, Name: "worker", NewToken: "tok-2", Nonce: "n-keepme",
		}, now); err != nil {
			t.Fatalf("replaying the historical register: %v", err)
		}
		if got := s.Agents[id].Nonce; got != "" {
			t.Errorf("a register op that predates restore_nonce put the nonce back "+
				"(nonce=%q). v0.0.6 left it blank, so the same ledger now folds to a "+
				"different board depending on which binary reads it, which is the "+
				"replay invariant failing across an upgrade", got)
		}
		// The consequence the reviewer actually measured: a later same-session
		// registration takes a different branch and mints a SIBLING, so the
		// agent set itself diverges rather than just one field.
		if got := len(s.Agents); got != before {
			t.Errorf("replaying a historical register changed the agent count "+
				"from %d to %d: the ledger reconstructs a different set of agents",
				before, got)
		}
	})

	// The same op as this version writes it. The behaviour is kept; only its
	// reach is bounded to ops that recorded the decision.
	t.Run("with the recorded decision, as v0.0.7 writes it", func(t *testing.T) {
		s, id, now := archiveAgent(t)
		if _, _, err := s.Apply(&Op{
			Kind: OpRegister, Name: "worker", NewToken: "tok-2", Nonce: "n-keepme",
			RestoreNonce: true,
		}, now); err != nil {
			t.Fatalf("register: %v", err)
		}
		if got := s.Agents[id].Nonce; got != "n-keepme" {
			t.Errorf("nonce = %q, want it restored. Gating the behaviour on the "+
				"recorded decision must not switch the behaviour off: an agent "+
				"recovered from archive needs its durable identity back, or a "+
				"declared role can never reconcile onto it again", got)
		}
	})
}
