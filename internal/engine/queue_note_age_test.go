// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// agenxy-design, 2026-10-22: send() said "Last native app notice: accepted"
// from an observation 18774s old, about an earlier message, while this
// message's own wake had failed. The note has to say how old it is.
func TestAStaleNativeAcceptanceSaysHowOldItIs(t *testing.T) {
	stale := queueWakeNote(core.Result{"native_delivery": "accepted", "observation_age_seconds": int64(18774)})
	if !strings.Contains(stale, "accepted, 5h12m ago, before this message") {
		t.Fatalf("a five-hour-old acceptance reads as current: %q", stale)
	}
	fresh := queueWakeNote(core.Result{"native_delivery": "accepted", "observation_age_seconds": int64(5)})
	if strings.Contains(fresh, "ago") || !strings.Contains(fresh, "Last native app notice: accepted") {
		t.Fatalf("a fresh acceptance changed wording: %q", fresh)
	}
}
