// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

var immediateClaudeArgv = []string{"/usr/bin/open", "claude://code/continue?session=local_x"}

// Preserve an old-code-compatible presence fixture: old RealShower has these
// fields; current production does not. Both enter RealShower's actual opener.
func absentPresenceFixture(s *Shower) {
	v := reflect.ValueOf(s).Elem()
	if field := v.FieldByName("Away"); field.IsValid() {
		field.Set(reflect.ValueOf(func() (bool, bool) { return false, false }))
	}
	if field := v.FieldByName("Idle"); field.IsValid() {
		field.Set(reflect.ValueOf(func() (time.Duration, bool) { return 0, false }))
	}
}

func TestClaudeRecoveryOpensImmediatelyThroughRealShower(t *testing.T) {
	s := RealShower
	var held atomic.Bool
	s.Holds = func(string) bool { return held.Load() }
	var waits atomic.Int32
	s.Wait = func(time.Duration) { waits.Add(1); held.Store(true) }
	absentPresenceFixture(&s)
	previous := appOpenFixture
	t.Cleanup(func() { appOpenFixture = previous })
	calls := 0
	appOpenFixture = func(mode, url string, _ time.Duration) error {
		calls++
		if mode != "claude-background" || url != immediateClaudeArgv[1] {
			t.Errorf("wrong native contact: %s %s", mode, url)
		}
		return nil
	}
	reports := 0
	s.ShowWhenIdle(immediateClaudeArgv, "immediate-claude", func(opened, deferred bool, err error) {
		reports++
		if !opened || deferred || err != nil {
			t.Errorf("closed recovery held: %v %v %v", opened, deferred, err)
		}
	})
	if calls != 1 || reports != 1 || waits.Load() != 0 || PendingAppOpen("immediate-claude") {
		t.Fatalf("immediate native contact: calls=%d reports=%d waits=%d", calls, reports, waits.Load())
	}
}

func TestImmediateClaudeRecoveryStillHonoursLoadedAndPermanentFailure(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "loaded"}[held], func(t *testing.T) {
			calls, reports := 0, 0
			s := Shower{
				Holds: func(string) bool { return held },
				Open:  func([]string) error { calls++; return errors.New("fixture failure") },
				Wait:  func(time.Duration) { t.Error("Claude recovery started a waiter") },
			}
			s.ShowWhenIdle(immediateClaudeArgv, "one-claude", func(opened, deferred bool, err error) {
				reports++
				if opened || deferred || err == nil {
					t.Error("unconfirmed recovery claimed success")
				}
			})
			want := 1
			if held {
				want = 0
			}
			if calls != want || reports != want || PendingAppOpen("one-claude") {
				t.Fatalf("calls=%d reports=%d", calls, reports)
			}
		})
	}
}
