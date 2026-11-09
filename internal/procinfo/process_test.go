// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package procinfo

import (
	"os"
	"testing"
	"time"
)

func TestSnapshotIncarnationIsRecheckedWithoutCrossPassCache(t *testing.T) {
	rows, err := Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var own Process
	for _, row := range rows {
		if row.PID == os.Getpid() {
			own = row
		}
	}
	if own.PID == 0 {
		t.Fatal("setup: current process absent")
	}
	if same, err := SameProcess(own); err != nil || !same {
		t.Fatalf("setup: current incarnation did not match: %t %v", same, err)
	}
	own.Start = own.Start.Add(-time.Minute)
	own.startTicks++
	if same, err := SameProcess(own); err != nil || same {
		t.Fatalf("stale incarnation matched a reused PID: %t %v", same, err)
	}
}
