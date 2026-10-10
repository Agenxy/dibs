// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/mcp"
)

// The capability enters at the actual producer's listen body, through the MCP
// server, into the derived bridge view. Calling an engine setter cannot prove it.
func TestBridgeAdvertisesPromptOpeningThroughTheActualListen(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "pre-away"}[legacy], func(t *testing.T) {
			b := bridgeUnderTest(t, &recordingRun{ok: true})
			body := b.listenBody()
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			meta := request["params"].(map[string]any)["_meta"].(map[string]any)
			if meta["com.dibs/native_app_delivery"] != true {
				t.Fatalf("production bridge omitted native app capability: %v", meta)
			}
			if meta["com.dibs/away_open"] != float64(3) {
				t.Fatalf("production bridge did not advertise prompt bounded opening: %v", meta)
			}
			if legacy {
				delete(meta, "com.dibs/away_open") // the pre-upgrade wire shape
				body, _ = json.Marshal(request)
			}
			// Subscription attachment is deliberately safe on the zero-value
			// engine: no writer query is issued without an event loop.
			eng := &engine.Engine{}
			srv := httptest.NewServer(mcp.New(eng))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("listen = %s", resp.Status)
			}
			scanner := bufio.NewScanner(resp.Body)
			if !scanner.Scan() {
				t.Fatal("no subscription acknowledgement")
			}
			bridges := eng.HostBridges()
			if len(bridges) != 1 || bridges[0].Host != b.host || len(bridges[0].Harnesses) != 1 {
				t.Fatalf("bridge route lost: %+v", bridges)
			}
			want := 3
			if legacy {
				want = 0
			}
			encoded, err := json.Marshal(bridges[0])
			if err != nil {
				t.Fatal(err)
			}
			var view map[string]any
			if err := json.Unmarshal(encoded, &view); err != nil {
				t.Fatal(err)
			}
			if view["away_open"] != float64(want) {
				t.Fatalf("bridge capability = %v; want %d", view["away_open"], want)
			}
		})
	}
}
