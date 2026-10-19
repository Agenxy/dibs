// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hostname

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheRefreshesNameAndRetainsLastGoodOnFailure(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Unix(100, 0).UnixNano())
	value := "first name"
	var failure error
	calls, fallbacks := 0, 0
	r := resolver{now: func() time.Time { return time.Unix(0, clock.Load()) }, lookup: func(context.Context) (string, error) { calls++; return value, failure }, fallback: func() (string, error) { fallbacks++; return "kernel", nil }}
	if got := r.name(); got != value {
		t.Fatal(got)
	}
	value = "renamed"
	if got := r.name(); got != "first name" || calls != 1 {
		t.Fatalf("cache did not hold label: %q calls%d", got, calls)
	}
	refreshForTest(t, &r, &clock)
	if got := r.name(); got != value || calls != 2 {
		t.Fatalf("cache did not refresh: %q calls%d", got, calls)
	}
	failure = errors.New("settings unavailable")
	refreshForTest(t, &r, &clock)
	if got := r.name(); got != "renamed" || fallbacks != 0 {
		t.Fatalf("refresh failure lost last good label: %q fallback%d", got, fallbacks)
	}
	failure = nil
	value = "new name"
	refreshForTest(t, &r, &clock)
	if got := r.name(); got != value {
		t.Fatalf("recovery retained stale name: %q", got)
	}
}

func TestInitialFailureFallsBackAndLaterRecovers(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Unix(100, 0).UnixNano())
	available := false
	r := resolver{now: func() time.Time { return time.Unix(0, clock.Load()) }, lookup: func(context.Context) (string, error) {
		if available {
			return "friendly", nil
		}
		return "", errors.New("unavailable")
	}, fallback: func() (string, error) { return "kernel", nil }}
	if got := r.name(); got != "kernel" {
		t.Fatal(got)
	}
	available = true
	refreshForTest(t, &r, &clock)
	if got := r.name(); got != "friendly" {
		t.Fatal(got)
	}
	available = false
	refreshForTest(t, &r, &clock)
	if got := r.name(); got != "friendly" {
		t.Fatalf("failure returned network kernel name again: %q", got)
	}
}

func TestColdFallbackIsNotCached(t *testing.T) {
	for _, idle := range []time.Duration{0, 30 * time.Second} {
		t.Run(idle.String(), func(t *testing.T) {
			now := time.Unix(100, 0)
			calls, fallbacks := 0, 0
			r := resolver{now: func() time.Time { return now }, lookup: func(ctx context.Context) (string, error) {
				calls++
				if calls == 1 {
					// A busy boot can exhaust the real shared lookup budget.
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "friendly", nil
			}, fallback: func() (string, error) { fallbacks++; return "network-kernel", nil }}
			if got := r.name(); got != "network-kernel" || calls != 1 || fallbacks != 1 {
				t.Fatalf("setup: first timeout got %q, calls=%d fallbacks=%d", got, calls, fallbacks)
			}
			// An expired cache also used to return the fallback once more,
			// starting its asynchronous refresh only after showing it again.
			now = now.Add(idle)
			if got := r.name(); got != "friendly" {
				t.Fatalf("cold fallback survived the next successful lookup: %q", got)
			}
			if got := r.name(); got != "friendly" || calls != 2 || fallbacks != 1 {
				t.Fatalf("successful friendly label was not cached: %q calls=%d fallbacks=%d", got, calls, fallbacks)
			}
		})
	}
}

func TestUnavailableNameDoesNotUseAnErroredFallback(t *testing.T) {
	unavailable := errors.New("unavailable")
	r := resolver{now: time.Now, lookup: func(context.Context) (string, error) { return "", unavailable }, fallback: func() (string, error) { return "invalid", unavailable }}
	if got := r.name(); got != "" {
		t.Fatalf("used a fallback returned with an error: %q", got)
	}
	if label := r.current(); label.Name != "" || !label.Fallback {
		t.Fatalf("failed snapshot=%+v", label)
	}
	waitForRefresh(t, &r)
}

func refreshForTest(t *testing.T, r *resolver, clock *atomic.Int64) {
	t.Helper()
	clock.Add(int64(cacheTTL))
	r.name() // the production expiry path starts the refresh
	r.mu.Lock()
	done := r.refreshing
	r.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("refresh did not complete")
		}
	}
}

func TestOneDeadlineBoundsTheDisplayLookup(t *testing.T) {
	r := resolver{now: time.Now, fallback: func() (string, error) { return "kernel", nil }, lookup: func(ctx context.Context) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > lookupBudget {
			t.Error("lookup has no shared bounded deadline")
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	start := time.Now()
	if got := r.name(); got != "kernel" {
		t.Fatal(got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("lookup exceeded bounded deadline by an order of magnitude")
	}
}

func TestConcurrentReadersShareOneLookup(t *testing.T) {
	calls := 0
	r := resolver{now: time.Now, fallback: func() (string, error) { return "kernel", nil }, lookup: func(context.Context) (string, error) { calls++; return "friendly", nil }}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if got := r.name(); got != "friendly" {
				t.Errorf("concurrent name %q", got)
			}
		})
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("concurrent cold readers made %d lookups", calls)
	}
}
