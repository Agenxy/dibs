package core

import (
	"testing"
	"time"
)

// Sweep events come out in the same order every time.
//
// Go randomises map iteration per process, so a sweep that marked eight agents
// stale at one serial emitted those eight events in one order live and a
// different order on cold replay. The replayed STATE was identical: this was
// never a fold failure, but the event stream is the audit history, and `agents
// log`, events_since and every consumer of the ledger read it. An audit trail
// that reorders itself when re-derived is not an audit trail.
//
// Asserted by running the same sweep on two independently built states, which is
// what a replay is. A single-run test cannot see this at all: within one process
// the iteration order is stable enough to look deterministic.
func TestSweepEmitsEventsInADeterministicOrder(t *testing.T) {
	build := func() *State {
		s := NewState("n1", DefaultLimits())
		s.Limits.IdleTTL = time.Second
		now := time.Now()
		for _, name := range []string{
			"order-0", "order-1", "order-2", "order-3",
			"order-4", "order-5", "order-6", "order-7",
		} {
			if _, _, err := s.Apply(&Op{
				Kind: OpRegister, Name: name, NewToken: "t-" + name, SessionID: name,
			}, now); err != nil {
				t.Fatalf("register %s: %v", name, err)
			}
		}
		return s
	}
	order := func(s *State) []string {
		// Far enough ahead that every agent crosses the idle bound in one sweep.
		// The daemon computes which agents have lapsed and passes them IN; the fold
		// does not consult a clock. Naming all eight makes one sweep emit eight
		// events at one serial, which is the case whose order was unstable.
		stale := []string{
			"order-0", "order-1", "order-2", "order-3",
			"order-4", "order-5", "order-6", "order-7",
		}
		_, evs, err := s.applySweep(&Op{Kind: OpSweep, StaleAgents: stale}, time.Now())
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		var out []string
		for _, e := range evs {
			if e.Agent != "" {
				out = append(out, e.Type+":"+e.Agent)
			}
		}
		return out
	}

	a, b := order(build()), order(build())
	if len(a) == 0 {
		t.Skip("this fixture produced no agent events; the ordering it guards is unreachable here")
	}
	if len(a) != len(b) {
		t.Fatalf("two identical states swept to different event counts: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("event %d differs between two runs of the same sweep: %q vs %q\n"+
				"  full A: %v\n  full B: %v", i, a[i], b[i], a, b)
			break
		}
	}
}

// Message expiry emits in a stable order too.
//
// The agent sweep was sorted first and this range was missed, which is the
// pattern worth guarding rather than the single site: any map traversal in the
// sweep that appends to evs reorders the audit stream on replay. Blob TTL
// eviction in blobs.go had the same shape and is sorted for the same reason.
func TestMessageExpiryEmitsInADeterministicOrder(t *testing.T) {
	build := func() *State {
		s := NewState("n1", DefaultLimits())
		past := time.Now().Add(-time.Hour)
		for i := uint64(1); i <= 8; i++ {
			s.Messages[i] = &Message{
				Serial: i, To: "recipient", From: "sender", Type: MsgQuestion,
				Body: "q", State: MsgStatePending, Deadline: past,
			}
		}
		return s
	}
	order := func(s *State) []uint64 {
		_, evs, err := s.applySweep(&Op{Kind: OpSweep}, time.Now())
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		var out []uint64
		for _, e := range evs {
			if serial, ok := e.Data["msg_serial"].(uint64); ok {
				out = append(out, serial)
			}
		}
		return out
	}
	a, b := order(build()), order(build())
	if len(a) == 0 {
		t.Skip("this fixture expired no messages; the ordering it guards is unreachable here")
	}
	for i := range a {
		if i < len(b) && a[i] != b[i] {
			t.Errorf("expiry event %d differs between two runs: %d vs %d\n  A %v\n  B %v",
				i, a[i], b[i], a, b)
			break
		}
	}
}
