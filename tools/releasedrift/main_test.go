// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"strings"
	"testing"
	"time"
)

// The case that motivated it: v0.0.13 tagged on 6 October, the first
// unreleased commit the next day, the operator asking on the 18th.
func TestTwelveDaysOfUnreleasedMainIsLate(t *testing.T) {
	tagged := time.Date(2026, 10, 6, 1, 53, 0, 0, time.UTC)
	now := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
	d := drift{tag: "v0.0.13", tagged: tagged, pending: 49, oldest: tagged.Add(26 * time.Hour)}
	msg, late := verdict(d, 7*24*time.Hour, now)
	if !late || !strings.Contains(msg, "49 commit(s)") || !strings.Contains(msg, "11 days") {
		t.Errorf("late=%v %q: twelve days of withheld fixes must fail loudly", late, msg)
	}
}

// Age is measured from the OLDEST unreleased commit, not the tag: a release
// that sat quiet for a month and then took one commit yesterday is not late.
func TestAgeRunsFromTheOldestUnreleasedCommit(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	d := drift{tag: "v0.0.14", tagged: now.Add(-30 * 24 * time.Hour), pending: 1, oldest: now.Add(-24 * time.Hour)}
	if msg, late := verdict(d, 7*24*time.Hour, now); late {
		t.Errorf("one day-old commit after an old tag was called late: %q", msg)
	}
}

func TestNothingPendingIsInStep(t *testing.T) {
	if msg, late := verdict(drift{tag: "v0.0.14"}, time.Hour, time.Now()); late || !strings.Contains(msg, "in step") {
		t.Errorf("late=%v %q", late, msg)
	}
}
