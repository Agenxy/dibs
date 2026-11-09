// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procinfo"
)

func TestNativeAppInventoryRejectsReusedRowsAndSharesOneFreshPass(t *testing.T) {
	rows, err := procinfo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var own procinfo.Process
	for _, row := range rows {
		if row.PID == os.Getpid() {
			own = row
		}
	}
	if own.PID == 0 {
		t.Fatal("setup: own kernel row missing")
	}
	currentStamp := procinfo.Stamp(own.Start)
	// This guard isolates row incarnation/cache behaviour. Real ancestry is
	// covered separately by the app/bridge subprocess fixture.
	own.PPID = 1
	old := nativeProcessSnapshot
	t.Cleanup(func() { nativeProcessSnapshot = old })
	calls := 0
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { calls++; return []procinfo.Process{own}, nil }
	claims := make([]BridgeIncarnation, 128)
	for i := range claims {
		claims[i] = BridgeIncarnation{PID: own.PID, Start: procinfo.Stamp(own.Start)}
	}
	observed, err := AppForBridges(t.Context(), claims)
	if err != nil || len(observed) != len(claims) || calls != 1 {
		t.Fatalf("batch did not share one production inventory: rows=%d calls=%d err=%v", len(observed), calls, err)
	}
	for _, row := range observed {
		if !row.Present {
			t.Fatal("valid native incarnation disappeared")
		}
	}
	// No cached inventory may bridge passes. A row with an older start is
	// rejected against the actual kernel even when the PID still exists.
	own.Start = own.Start.Add(-time.Minute)
	observed, err = AppForBridges(t.Context(), []BridgeIncarnation{{PID: own.PID, Start: procinfo.Stamp(own.Start)}})
	if err != nil || len(observed) != 1 || observed[0].Present || calls != 2 {
		t.Fatalf("a reused row survived a fresh pass: %+v calls=%d err=%v", observed, calls, err)
	}
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { calls++; return nil, nil }
	observed, err = AppForBridges(t.Context(), []BridgeIncarnation{{PID: own.PID, Start: currentStamp}})
	if err == nil || calls != 3 {
		t.Fatalf("a live PID missing from a fresh inventory did not stay unknown: %+v calls=%d err=%v", observed, calls, err)
	}
}
