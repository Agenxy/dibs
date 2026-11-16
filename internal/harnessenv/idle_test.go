// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"testing"
	"time"
)

// Presence cannot make an activating URL safe. The refusal enters the same
// ShowWhenIdle route old bridges used, with only native contact replaced.
func TestClaudeRecoveryRefusesNavigationThroughRealShower(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "ended", true: "already-running"}[held], func(t *testing.T) {
			s := RealShower
			s.Holds = func(string) bool { return held }
			s.Wait = func(time.Duration) { t.Fatal("retired route started a waiter") }
			previous := appOpenFixture
			calls := 0
			appOpenFixture = func(string, string, time.Duration) error { calls++; return nil }
			t.Cleanup(func() { appOpenFixture = previous })
			reports := 0
			s.ShowWhenIdle(ClaudeOpenArgv("local_background-fixture"), "claude-fixture", func(opened, deferred bool, err error) {
				reports++
				if opened || deferred || err == nil {
					t.Errorf("retired Claude URL was not held: %v %v %v", opened, deferred, err)
				}
			})
			if calls != 0 || reports != 1 || PendingAppOpen("claude-fixture") {
				t.Fatalf("contact=%d reports=%d", calls, reports)
			}
		})
	}
}
