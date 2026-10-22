// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// #482: on the hosted macOS gate the kernel lookup of the bridge's own start
// stamp twice took past five seconds, inline on the first request, and the
// agent's first register got no reply. The stamp is additive: a request must
// not wait on it past the budget, and a hanging lookup must not multiply.
func TestASlowStartStampDoesNotHoldTheFirstRequest(t *testing.T) {
	release := make(chan struct{})
	var lookups atomic.Int32
	previous := lookupSelfStart
	lookupSelfStart = func() string {
		lookups.Add(1)
		<-release
		return "Thu Oct 22 15:59:20 2026"
	}
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

	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{}}}`)
	for range 3 {
		started := time.Now()
		done := make(chan []byte, 1)
		go func() { done <- enrichBridgeProcess(line) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a request is still waiting on a start-stamp lookup that never returns")
		}
		if took := time.Since(started); took > 2*bridgeStartBudget {
			t.Fatalf("a request waited %s for the additive stamp; the budget is %s", took, bridgeStartBudget)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("%d lookups started while the first was still in flight; want 1", n)
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for bridgeStarted() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the stamp never arrived after the lookup returned")
		}
	}
	if got := bridgeStarted(); got != "Thu Oct 22 15:59:20 2026" {
		t.Fatalf("stamp %q", got)
	}
}
