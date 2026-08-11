package ledger

import (
	"testing"

	"github.com/agenxy/lanes/internal/core"
)

// The whole-state comparison in TestRandomizedReplayEquivalence is only worth
// anything if it can fail. A comparison that silently marshals to "{}": a
// MarshalJSON somewhere, an unexported field, a nil map: passes every run and
// reads exactly like a proof of correctness.
func TestStateDiffIsNotVacuous(t *testing.T) {
	_, path := newLedger(t)
	st := reopen(t, path)
	st2 := reopen(t, path)
	if d := stateDiff(st, st2); d != "" {
		t.Fatalf("two identical replays differ: %s", d)
	}
	st2.Serial = 999
	if stateDiff(st, st2) == "" {
		t.Fatal("stateDiff sees no difference between serial 0 and serial 999: it compares nothing")
	}
	st2 = reopen(t, path)
	st2.Channels["ghost"] = &core.Channel{ID: "ghost", Members: map[string]*core.Membership{}, Subs: map[string]bool{}}
	if stateDiff(st, st2) == "" {
		t.Fatal("stateDiff missed an entire extra channel")
	}
}
