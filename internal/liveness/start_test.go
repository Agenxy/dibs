// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package liveness

import (
	"os"
	"testing"
	"time"
)

// Two readings of one process agree exactly, or the sweep's equality check
// would read every live agent as a recycled pid.
func TestStartMillisIsStableAndInThePast(t *testing.T) {
	p := New()
	a, ok := p.StartMillis(os.Getpid())
	if !ok {
		t.Fatal("no start time for this very process")
	}
	b, _ := p.StartMillis(os.Getpid())
	if a != b {
		t.Fatalf("two readings disagree: %d then %d", a, b)
	}
	if now := time.Now().UnixMilli(); a <= 0 || a > now {
		t.Fatalf("start %d is not in the past (now %d)", a, now)
	}
	if _, ok := p.StartMillis(0); ok {
		t.Error("pid 0 has a start time")
	}
}
