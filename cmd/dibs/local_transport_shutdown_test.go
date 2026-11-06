// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixtureShutdownAbortsLateRequests(t *testing.T) {
	var calls atomic.Int32
	srv := newLocalTestServer(t, cliIPCProfile(t), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	// Retain the exact fixture admission wrapper on a live listener, modelling
	// a request dispatched to it after Close sets its fence but before all
	// connections stop. Enter through real HTTP and real Close, never by setting
	// the closing flag. The listener keeps the outcome observable rather than
	// letting connection closure hide an empty success from this wrapper.
	late := httptest.NewServer(srv.Config.Handler)
	t.Cleanup(late.Close)
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get(late.URL)
	if err != nil {
		t.Fatal("setup: live fixture wrapper request failed:", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || calls.Load() != 1 {
		t.Fatal("setup: live request did not reach the actual fixture handler")
	}
	srv.Close()
	resp, err = client.Get(late.URL)
	if resp != nil {
		_ = resp.Body.Close()
		t.Errorf("post-fence request received HTTP %d instead of a transport error", resp.StatusCode)
	}
	if err == nil {
		t.Error("post-fence request succeeded instead of aborting its connection")
	}
	if calls.Load() != 1 {
		t.Error("post-fence request reached the closed fixture handler")
	}
}
