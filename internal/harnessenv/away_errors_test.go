package harnessenv

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/notify"
)

func TestAwayOpenPermanentFailureStopsAfterOneAttempt(t *testing.T) {
	var attempts, reports, waits atomic.Int32
	s := Shower{
		Holds: func(string) bool { return false },
		Away:  func() (bool, bool) { return true, true },
		Open:  func([]string) error { attempts.Add(1); return errors.New("helper unavailable") },
		Wait:  func(time.Duration) { waits.Add(1) },
	}
	s.ShowWhenIdle([]string{"/usr/bin/open", "claude://code/continue?session=local_test"}, "permanent-error-test", func(bool, bool, error) {
		reports.Add(1)
	})
	if attempts.Load() != 1 || reports.Load() != 1 || PendingAppOpen("permanent-error-test") {
		t.Fatalf("permanent error retried: attempts=%d reports=%d waits=%d pending=%v",
			attempts.Load(), reports.Load(), waits.Load(), PendingAppOpen("permanent-error-test"))
	}
}

func TestAwayOpenPresenceRaceWaitsQuietly(t *testing.T) {
	var attempts, errorsReported atomic.Int32
	settled := make(chan struct{})
	s := Shower{
		Holds: func(string) bool { return false },
		Away:  func() (bool, bool) { return true, true },
		Open: func([]string) error {
			if attempts.Add(1) <= 3 {
				return notify.ErrNotAway
			}
			return nil
		},
		Wait: func(time.Duration) {},
	}
	s.ShowWhenIdle([]string{"/usr/bin/open", "claude://code/continue?session=local_test"}, "presence-race-test",
		func(opened, _ bool, err error) {
			if err != nil {
				errorsReported.Add(1)
			}
			if opened {
				close(settled)
			}
		})
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("presence race did not resume waiting")
	}
	if attempts.Load() != 4 || errorsReported.Load() != 0 {
		t.Fatalf("presence race spammed failures: attempts=%d errors=%d", attempts.Load(), errorsReported.Load())
	}
}
