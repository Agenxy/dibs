package harnessenv

import (
	"sync/atomic"
	"testing"
	"time"
)

// Enter the production Shower, not a fixture with the new gate injected.
// Unknown HID idle cannot authorize an open even with a zero threshold.
// The source-build native helper is missing, so presence is unknown and the
// actual production gate must fail closed.
func TestRealShowerClaudeRecoveryDoesNotOpenOnUnknownPresence(t *testing.T) {
	s := RealShower
	var opened atomic.Int32
	var held atomic.Bool
	release := make(chan struct{})
	finished := make(chan struct{})
	s.Holds = func(string) bool { return held.Load() }
	s.Open = func([]string) error { opened.Add(1); return nil }
	s.Idle = func() (time.Duration, bool) { return time.Hour, false }
	s.MinIdle = 0
	s.Poll = time.Millisecond
	s.Wait = func(time.Duration) { <-release; held.Store(true); close(finished) }
	var deferred bool
	s.ShowWhenIdle(argv, "away-production-fixture", func(_, def bool, _ error) { deferred = def })
	if opened.Load() != 0 || !deferred {
		t.Fatalf("idle or zero threshold opened a visible app: opened=%d deferred=%v", opened.Load(), deferred)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("waiter did not see externally loaded thread")
	}
}

// Recent input defers an open until positive lock or sleep evidence appears.
func TestAwayGateWaitsForPositiveEvidence(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "present"}[known], func(t *testing.T) {
			var away atomic.Bool
			var opens atomic.Int32
			done := make(chan struct{})
			s := Shower{
				Holds: func(string) bool { return false },
				Open:  func([]string) error { opens.Add(1); return nil },
				Away: func() (bool, bool) {
					if away.Load() {
						return true, true
					}
					return false, known
				},
				MinIdle: 10 * time.Minute,
				Idle:    func() (time.Duration, bool) { return time.Second, true },
				Poll:    time.Millisecond,
				Wait:    func(time.Duration) { away.Store(true) },
			}
			s.ShowWhenIdle(argv, "away-"+t.Name(), func(opened, deferred bool, err error) {
				if !deferred {
					if !opened || err != nil {
						t.Errorf("away open: %v %v", opened, err)
					}
					close(done)
				}
			})
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("away thread never opened")
			}
			if opens.Load() != 1 {
				t.Errorf("opens=%d", opens.Load())
			}
		})
	}
}
