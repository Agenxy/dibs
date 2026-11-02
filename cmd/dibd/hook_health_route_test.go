// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
)

func TestHookHealthRouteReadsTheActualEngine(t *testing.T) {
	eng := engineNamed(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	mux := http.NewServeMux()
	registerMatchStatusAPI(mux, eng)
	read := func() engine.HookHealth {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/hook-health", nil))
		var h engine.HookHealth
		if err := json.Unmarshal(response.Body.Bytes(), &h); err != nil || response.Code != http.StatusOK {
			t.Fatalf("actual hook-health route: status=%d error=%v", response.Code, err)
		}
		return h
	}
	if h := read(); h.Verdict != "observing" {
		t.Fatalf("route did not read the new engine: %+v", h)
	}
	if _, err := eng.HookPoll(ctx, "unregistered-fixture-session", "Stop", "/fixture", false, false); err != nil {
		t.Fatal(err)
	}
	if h := read(); h.PollStrangers != 1 || h.Verdict != "only-strangers" || h.Last.IsZero() {
		t.Fatalf("route did not read real hook activity: %+v", h)
	}
}
