package core

import (
	"sort"
	"testing"
	"time"
)

// A sweep that only DELETES must still be ledgered.
//
// gc removes consumed mail and expired dedup records without emitting an event,
// which is right: nobody needs telling that a message the sender already read
// has aged out. But applySweep tested "did we emit anything", so a sweep whose
// only work was those deletions reported changed:false, was never written to the
// ledger, and did not advance the serial.
//
// Replay therefore never performed the deletion, and consumed mail RESURRECTED
// on restart. That is not a tidiness issue: state stopped being fold(ledger),
// which is the one claim this design exists to keep. Silent to an observer and
// silent to the ledger are different things, and only the first was intended.
func TestASweepThatOnlyDeletesIsStillRecorded(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()

	// A consumed terminal message, aged past its retention.
	s.Messages[5] = &Message{
		Serial: 5, To: "a", From: "b", Type: MsgNotify, Body: "read and done",
		State: MsgStateAcked, Consumed: true,
		TerminalAt: now.Add(-2 * s.Limits.ConsumedRetention),
	}

	res, evs, err := s.applySweep(&Op{Kind: OpSweep}, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, still := s.Messages[5]; still {
		t.Fatal("setup: the consumed message was not pruned, so this test proves nothing")
	}
	changed, _ := res["changed"].(bool)
	if !changed {
		t.Error("a sweep that deleted a message reported changed:false: the op is not " +
			"ledgered, the serial does not advance, and replay resurrects the mail")
	}
	if len(evs) == 0 {
		// Not required: the deletion is deliberately quiet. The serial advancing
		// is what matters, and finish() is what does it.
		t.Log("no events emitted, as intended; the sweep is recorded via changed:true")
	}
}

// The same for dedup records, which expire without announcing themselves.
func TestExpiredDedupRecordsAreRecordedToo(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	s.Dedup[dedupKey("a", "op-1")] = &DedupRec{
		Agent: "a", ID: "op-1", At: now.Add(-2 * s.Limits.DedupWindow),
	}
	res, _, err := s.applySweep(&Op{Kind: OpSweep}, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, still := s.Dedup[dedupKey("a", "op-1")]; still {
		t.Fatal("setup: the record was not expired")
	}
	if changed, _ := res["changed"].(bool); !changed {
		t.Error("expiring a dedup record reported changed:false: on replay the record " +
			"survives, so a retry that was deduplicated live is accepted again")
	}
}

// Equal timestamps must not make the cap keep an arbitrary subset.
//
// Dedup records come out of a map, so ties left them in random order and the cap
// preserved a different set live than on replay. Which retry deduplicates then
// depends on map iteration: idempotency that is only sometimes idempotent,
// which is worse than none because the caller was told the retry was safe.
func TestDedupEvictionIsDeterministicWhenTimestampsTie(t *testing.T) {
	build := func() *State {
		s := NewState("n1", DefaultLimits())
		s.Limits.DedupPerAgent = 2
		at := time.Now().Add(-time.Minute) // identical for every record
		for _, id := range []string{"op-1", "op-2", "op-3", "op-4", "op-5"} {
			s.Dedup[dedupKey("a", id)] = &DedupRec{Agent: "a", ID: id, At: at}
		}
		return s
	}
	survivors := func(s *State) []string {
		s.gc(time.Now())
		var out []string
		for _, rec := range s.Dedup {
			out = append(out, rec.ID)
		}
		sort.Strings(out)
		return out
	}
	a, b := survivors(build()), survivors(build())
	if len(a) != len(b) {
		t.Fatalf("different survivor counts: %v vs %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("the same sweep retained different idempotency records:\n  A %v\n  B %v", a, b)
		}
	}
}
