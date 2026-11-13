// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestFallbackCounterConcurrentAndSaturating(t *testing.T) {
	var counter atomic.Uint64
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			for range 32 {
				nextFallbackCount(&counter)
			}
		})
	}
	workers.Wait()
	if got := counter.Load(); got != 1024 {
		t.Fatalf("concurrent fallback count=%d, want 1024", got)
	}
	counter.Store(^uint64(0) - 1)
	for range 2 {
		if got := nextFallbackCount(&counter); got != ^uint64(0) {
			t.Fatalf("fallback count wrapped: %d", got)
		}
	}
}
