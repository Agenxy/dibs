// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hostname

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDisplayNameReadsDoNotProbeEveryIdleTick(t *testing.T) {
	var clock, calls atomic.Int64
	clock.Store(time.Unix(100, 0).UnixNano())
	r := resolver{now: func() time.Time { return time.Unix(0, clock.Load()) }, fallback: func() (string, error) { return "kernel", nil }, lookup: func(context.Context) (string, error) { calls.Add(1); return "friendly", nil }}
	if got := r.name(); got != "friendly" {
		t.Fatal("setup:", got)
	}
	for range 60 {
		clock.Add(int64(time.Second))
		r.current()
		waitForRefresh(t, &r)
	}
	if calls.Load() != 1 {
		t.Fatalf("idle display reads repeated the native helper %d times in one minute", calls.Load())
	}
	clock.Add(int64(5 * time.Minute))
	r.current()
	waitForRefresh(t, &r)
	if calls.Load() != 2 {
		t.Fatalf("request-driven refresh did not see expiry: %d", calls.Load())
	}
}

func TestColdFailureBacksOffWithoutCachingFallbackOrBlocking(t *testing.T) {
	var clock, calls atomic.Int64
	clock.Store(time.Unix(100, 0).UnixNano())
	r := resolver{now: func() time.Time { return time.Unix(0, clock.Load()) }, fallback: func() (string, error) { return "kernel", nil }, lookup: func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("cold lookup failed")
		}
		return "friendly", nil
	}}
	if got := r.name(); got != "kernel" {
		t.Fatal("setup:", got)
	}
	for range 100 {
		if label := r.current(); label.Name != "kernel" || !label.Fallback {
			t.Fatalf("fallback became a friendly observation: %+v", label)
		}
		waitForRefresh(t, &r)
	}
	if calls.Load() != 1 {
		t.Fatalf("a cold failure caused %d immediate retries", calls.Load())
	}
	returned := make(chan string, 1)
	go func() { returned <- r.name() }()
	select {
	case got := <-returned:
		if got != "kernel" {
			t.Fatal(got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("backoff made a cold Name wait on a nil refresh channel")
	}
	clock.Add(int64(30 * time.Second))
	r.current()
	waitForRefresh(t, &r)
	if label := r.current(); label.Name != "friendly" || label.Fallback || calls.Load() != 2 {
		t.Fatalf("backoff did not recover through the next request: %+v calls=%d", label, calls.Load())
	}
}
