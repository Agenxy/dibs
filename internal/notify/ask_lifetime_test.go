// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"testing"
	"time"
)

func TestDetailedAskLifetimePreservesRequestDeadlineAndCapsUnknown(t *testing.T) {
	now := time.Unix(1700000000, 123456789)
	for _, tc := range []struct {
		name           string
		deadline, want time.Time
	}{
		{"unknown", time.Time{}, now.Add(24 * time.Hour)},
		{"short", now.Add(time.Hour), now.Add(time.Hour)},
		{"long", now.Add(7 * 24 * time.Hour), now.Add(24 * time.Hour)},
		{"expired", now.Add(-time.Second), now.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := askUntil(tc.deadline, now); !got.Equal(tc.want) {
				t.Fatalf("until %v want %v", got, tc.want)
			}
		})
	}
}
