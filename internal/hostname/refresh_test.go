// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hostname

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestExpiredNameReturnsImmediatelyWithOnlyOneRefresh(t *testing.T) {
	var clock, calls atomic.Int64
	clock.Store(time.Now().UnixNano())
	entered, release := make(chan struct{}), make(chan struct{})
	r := resolver{now: func() time.Time { return time.Unix(0, clock.Load()) }, fallback: func() (string, error) { return "kernel", nil }, lookup: func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "friendly", nil
		}
		close(entered)
		<-release
		return "renamed", nil
	}}
	t.Cleanup(func() {
		close(release)
		r.mu.Lock()
		done := r.refreshing
		r.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("refresh did not finish after fixture release")
			}
		}
	})
	if got := r.name(); got != "friendly" {
		t.Fatal("setup:", got)
	}
	clock.Add(int64(cacheTTL))
	returned := make(chan string, 1)
	go func() { returned <- r.name() }()
	select {
	case got := <-returned:
		if got != "friendly" {
			t.Fatal(got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expired display blocked behind lookup")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	for range 16 {
		if got := r.name(); got != "friendly" {
			t.Fatal(got)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("more than one refresh in flight: %d", calls.Load())
	}
	// Cleanup releases the deliberately uncooperative lookup; Name never waits for it.
}
