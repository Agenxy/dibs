// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hostname

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestColdSnapshotDoesNotWaitForLookupOrRegistration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	r := resolver{now: time.Now, fallback: func() (string, error) { return "network-kernel", nil }, lookup: func(context.Context) (string, error) {
		calls.Add(1)
		close(entered)
		<-release
		return "friendly", nil
	}}
	registration := make(chan string, 1)
	go func() { registration <- r.name() }()
	t.Cleanup(func() {
		close(release)
		select {
		case <-registration:
		case <-time.After(time.Second):
			t.Error("registration did not finish after releasing the lookup")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("setup: registration did not enter the lookup")
	}
	read := make(chan Label, 1)
	go func() {
		// A whole board's worth of reads must not execute a lookup on its
		// caller or wait for the registration's in-flight cold lookup.
		var label Label
		for range 40 {
			label = r.current()
		}
		read <- label
	}()
	select {
	case label := <-read:
		if label.Name != "network-kernel" || !label.Fallback || calls.Load() != 1 {
			t.Fatalf("cold snapshot=%+v lookup calls=%d", label, calls.Load())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("snapshot waited for a subprocess lookup")
	}
}

func TestColdSnapshotRetriesAfterTimeoutWithoutCachingFallback(t *testing.T) {
	for _, idle := range []time.Duration{0, 30 * time.Second} {
		t.Run(idle.String(), func(t *testing.T) {
			now := time.Unix(100, 0)
			var calls atomic.Int64
			entered, release := make(chan struct{}), make(chan struct{})
			releaseLookup := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseLookup)
			r := resolver{now: func() time.Time { return now }, fallback: func() (string, error) { return "network-kernel", nil }, lookup: func(ctx context.Context) (string, error) {
				if calls.Add(1) == 1 {
					<-ctx.Done()
					return "", ctx.Err()
				}
				close(entered)
				<-release
				return "friendly", nil
			}}
			if label := r.current(); label.Name != "network-kernel" || !label.Fallback {
				t.Fatalf("initial snapshot=%+v", label)
			}
			waitForRefresh(t, &r)
			now = now.Add(idle)
			if label := r.current(); label.Name != "network-kernel" || !label.Fallback {
				t.Fatalf("snapshot before success=%+v", label)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("fallback was cached instead of retrying")
			}
			releaseLookup()
			waitForRefresh(t, &r)
			if label := r.current(); label.Name != "friendly" || label.Fallback || calls.Load() != 2 {
				t.Fatalf("next snapshot=%+v calls=%d", label, calls.Load())
			}
		})
	}
}

func waitForRefresh(t *testing.T, r *resolver) {
	t.Helper()
	r.mu.Lock()
	done := r.refreshing
	r.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("lookup did not finish")
		}
	}
}

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
