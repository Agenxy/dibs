// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSurfaceCompletionRetriesFailureOnceAndRetainsUnknown(t *testing.T) {
	previous := detectedSurface
	t.Cleanup(func() { detectedSurface = previous })
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		detectedSurface = func(context.Context) (string, error) {
			calls++
			if canceled {
				cancel()
			}
			return "", context.DeadlineExceeded
		}
		c := &surfaceCompletion{
			pending: map[string]surfaceRepair{"own-row": {token: "own-token", codex: true}},
			probe:   &surfaceProbe{ctx: ctx},
		}
		c.advance(ctx, http.DefaultClient, "not-a-board", "placeholder")
		cancel()
		want := 2
		if canceled {
			want = 1
		}
		if calls != want || len(c.pending) != 1 || c.unsupported {
			t.Fatalf("canceled=%v: failed inventory attempts=%d, want %d; pending=%d unsupported=%v",
				canceled, calls, want, len(c.pending), c.unsupported)
		}
		if surface, known := c.probe.result(); known || surface != "" {
			t.Fatalf("failed discovery became routing evidence %q known=%v", surface, known)
		}
	}
}

func TestStartCompletionRetriesFailureOnceAndStopsWithSession(t *testing.T) {
	previous := lookupSelfStart
	bridgeStart.Lock()
	previousAt := bridgeStart.at
	bridgeStart.Unlock()
	t.Cleanup(func() {
		lookupSelfStart = previous
		bridgeStart.Lock()
		bridgeStart.at, bridgeStart.pending = previousAt, nil
		bridgeStart.Unlock()
	})
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		attempted := make(chan struct{}, 2)
		lookupSelfStart = func() string {
			calls.Add(1)
			if canceled {
				cancel()
			}
			attempted <- struct{}{}
			return ""
		}
		bridgeStart.Lock()
		bridgeStart.at, bridgeStart.pending = "", nil
		bridgeStart.Unlock()
		omitted := make(chan struct{}, 1)
		omitted <- struct{}{}
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			deliverBridgeStart(ctx, http.DefaultClient, "not-a-board", "placeholder", omitted)
		}()
		want := int32(2)
		if canceled {
			want = 1
		}
		timedOut := false
		for range want {
			select {
			case <-attempted:
			case <-time.After(time.Second):
				timedOut = true
			}
		}
		cancel()
		<-ended
		// Join the lookup too before resetting its package-level seam.
		bridgeStart.Lock()
		pending := bridgeStart.pending
		bridgeStart.Unlock()
		if pending != nil {
			<-pending
		}
		if timedOut || calls.Load() != want {
			t.Fatalf("canceled=%v: failed stamp attempts=%d, want %d", canceled, calls.Load(), want)
		}
	}
}

func TestCompletedStartBudgetDoesNotGrowAfterTwoFailures(t *testing.T) {
	previous := lookupSelfStart
	bridgeStart.Lock()
	previousAt := bridgeStart.at
	bridgeStart.at, bridgeStart.pending = "", nil
	bridgeStart.Unlock()
	t.Cleanup(func() {
		lookupSelfStart = previous
		bridgeStart.Lock()
		bridgeStart.at, bridgeStart.pending = previousAt, nil
		bridgeStart.Unlock()
	})
	calls := 0
	lookupSelfStart = func() string { calls++; return "" }
	if got := completedBridgeStart(context.Background()); got != "" || calls != 2 {
		t.Fatalf("two failed stamp lookups must stop completion: stamp=%q attempts=%d", got, calls)
	}
}
