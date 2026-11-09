// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// GitHub's release download path answered 504 to roughly half of all requests
// for this repository's assets for most of an hour on 2026-11-09, while the
// same bytes were one retry away. An upgrade, and the install check that
// proves a candidate, failed on the first 504 every time. A gateway error is
// retried a bounded number of times; anything that is an answer (a 404, a
// 403) is not, because asking again cannot change it.
func TestAGatewayErrorIsRetriedAndAnAnswerIsNot(t *testing.T) {
	defer func(d []time.Duration) { retryDelays = d }(retryDelays)
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}

	serve := func(statuses ...int) (*httptest.Server, *atomic.Int32) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := int(hits.Add(1)) - 1
			if n < len(statuses) {
				w.WriteHeader(statuses[n])
				return
			}
			_, _ = w.Write([]byte("the asset"))
		}))
		t.Cleanup(srv.Close)
		return srv, &hits
	}

	srv, hits := serve(http.StatusGatewayTimeout, http.StatusBadGateway)
	body, err := fetchBody(context.Background(), srv.Client(), srv.URL+"/checksums.txt")
	if err != nil {
		t.Fatalf("two gateway errors then the asset must succeed: %v", err)
	}
	got, _ := io.ReadAll(body)
	_ = body.Close()
	if string(got) != "the asset" || hits.Load() != 3 {
		t.Fatalf("got %q after %d requests, want the asset after 3", got, hits.Load())
	}

	srv, hits = serve(http.StatusNotFound)
	if _, err := fetchBody(context.Background(), srv.Client(), srv.URL+"/checksums.txt"); err == nil {
		t.Fatal("a 404 is an answer and must fail")
	}
	if hits.Load() != 1 {
		t.Fatalf("a 404 was asked %d times; asking again cannot change it", hits.Load())
	}

	srv, hits = serve(http.StatusGatewayTimeout, http.StatusGatewayTimeout, http.StatusGatewayTimeout, http.StatusGatewayTimeout)
	_, err = fetchBody(context.Background(), srv.Client(), srv.URL+"/checksums.txt")
	if err == nil {
		t.Fatal("an outage that outlasts the retries must still fail")
	}
	if hits.Load() != 3 {
		t.Fatalf("the retry is bounded at 3 attempts; made %d", hits.Load())
	}
	if !strings.Contains(err.Error(), "checksums.txt") || !strings.Contains(err.Error(), "504") ||
		!strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("the error has to name the URL, the status and that it retried; got %q", err)
	}
}

// A cancelled upgrade stops at once rather than sleeping through its backoff.
func TestACancelledFetchDoesNotWaitOutItsRetries(t *testing.T) {
	defer func(d []time.Duration) { retryDelays = d }(retryDelays)
	retryDelays = []time.Duration{time.Hour, time.Hour}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := fetchBody(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("a cancelled fetch must fail")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("a cancelled fetch waited %s", time.Since(start))
	}
}
