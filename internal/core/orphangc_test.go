package core

import (
	"testing"
	"time"
)

// A deletion the fold performs must advance the serial, even when nobody is
// left to be told about it.
//
// Mail can outlive its recipient. A `purge_mail:false` sweep, which is what
// every sweep written before v0.0.7 was, removes the agent row and leaves the
// mail behind on purpose. Retention then evicts those messages later with no
// row to attribute them to, so the eviction emitted no event and set no flag:
// the sweep reported changed:false, the engine did not ledger it, and the next
// restart replayed a board where the messages still existed.
//
// That is state == fold(ledger) failing in the quietest possible way. Nothing
// errors, nothing logs, and the mail comes back on a bounce. Found by the
// pre-release review.
func TestEvictingOrphanedMailIsLedgered(t *testing.T) {
	s := NewState("t", DefaultLimits())
	s.Limits.TerminalRetention = 1
	now := time.Unix(1700000000, 0)

	// Two terminal messages for an agent that no longer has a row, which is
	// exactly what a historical sweep leaves behind.
	for i := uint64(1); i <= 2; i++ {
		s.Messages[i] = &Message{
			Serial: i, From: "sender", To: "purged", Type: MsgNotify,
			State: MsgStateDisplaced, TerminalAt: now.Add(-time.Hour),
		}
	}
	if s.Agents["purged"] != nil {
		t.Fatal("setup: the recipient row must be gone, which is the whole case")
	}
	before := s.Serial

	res, _, err := s.Apply(&Op{Kind: OpSweep, PurgeMail: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Setup must hold: an eviction has to have happened, or "changed" being
	// false would be correct and this would assert nothing.
	if len(s.Messages) != 1 {
		t.Fatalf("setup: %d message(s) left, expected one eviction", len(s.Messages))
	}

	if changed, _ := res["changed"].(bool); !changed {
		t.Error("the sweep deleted mail and reported changed:false. The engine " +
			"ledgers exactly when the serial advanced, so this deletion is never " +
			"written down and the next restart replays a board that still has the " +
			"messages: deleted in memory, alive on disk, back after a bounce")
	}
	if s.Serial == before {
		t.Errorf("the serial did not advance (%d) for a change to replayable state",
			s.Serial)
	}
}

// And a sweep that genuinely changes nothing still reports so, or every idle
// tick would append an op and the ledger would grow without end.
func TestASweepThatChangesNothingIsNotLedgered(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	before := s.Serial
	res, evs, err := s.Apply(&Op{Kind: OpSweep, PurgeMail: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := res["changed"].(bool); changed || len(evs) != 0 {
		t.Errorf("an empty board reported a change (%v, %d events): every idle "+
			"sweep would be ledgered and the ledger would grow forever", res, len(evs))
	}
	if s.Serial != before {
		t.Errorf("the serial advanced (%d to %d) for a sweep that changed nothing",
			before, s.Serial)
	}
}
