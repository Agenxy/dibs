// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
)

// Enter the actual IPC handler wrapper and fixture Close used by the ancestry
// contract. Hold a real callback beyond connection closure: only joining the
// callback protects the evidence mapping that t.Cleanup releases next.
func TestFixtureCloseJoinsAncestryEvidenceCallbacks(t *testing.T) {
	dir := cliIPCProfile(t)
	evidence := newAncestryEvidence(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	inner := evidence.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	srv := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		inner.ServeHTTP(w, r)
	}))
	var once sync.Once
	releaseCallback := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseCallback)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localtransport.Dial(ctx, dir)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := (&http.Client{Transport: transport}).Get("http://fixture.invalid/evidence")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: actual IPC callback never entered")
	}
	closed := make(chan struct{})
	go func() { srv.Close(); close(closed) }()
	// Positive evidence of the join, not a sleep that hopes Close had time
	// to return. The old Close returns; the fixed Close waits on this actual
	// blocked callback. This observer exists only in this dedicated guard.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	stack := make([]byte, 1<<20)
observe:
	for {
		select {
		case <-closed:
			t.Error("fixture Close returned while the evidence callback was still active")
			break observe
		case <-deadline.C:
			t.Error("fixture Close neither returned nor joined the held callback")
			break observe
		default:
		}
		n := runtime.Stack(stack, true)
		for _, g := range bytes.Split(stack[:n], []byte("\n\n")) {
			if bytes.Contains(g, []byte("(*localTestServer).Close")) && bytes.Contains(g, []byte("sync.(*WaitGroup).Wait")) &&
				!bytes.Contains(g, []byte("net/http.(*Server).Close")) &&
				!bytes.Contains(g, []byte("net/http/httptest.(*Server).Close")) {
				break observe
			}
		}
		runtime.Gosched()
	}
	releaseCallback()
	for _, done := range []<-chan struct{}{finished, closed, clientDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("fixture did not finish after releasing its callback")
		}
	}
	if atomic.LoadInt64(ancestryCell(evidence.memory, 0, int(ancestryHandlerReturned)+1)) == 0 {
		t.Fatal("fixture joined before the real evidence callback stamped its return")
	}
}
