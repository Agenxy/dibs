// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

// Hold the first real receipt response while another authored item arrives.
// The shared writer must reconsider that item after settlement, never discard
// it because another callback temporarily owns the reservation.
func TestBridgeKeepsMailArrivingDuringAnInflightReceipt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var gate, cleanup sync.Once
	t.Cleanup(func() { cleanup.Do(func() { close(release) }) })
	f := newEconomyFixtureWith(t, "71f97290-0001-4000-8000-555555555555", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error("setup request read:", err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(b))
				var req struct {
					Method string `json:"method"`
					Params struct {
						Meta map[string]any `json:"_meta"`
					} `json:"params"`
				}
				if json.Unmarshal(b, &req) == nil && req.Method == "resources/read" && req.Params.Meta[mcp.SocketWrittenMetaKey] == true {
					gate.Do(func() { close(entered); <-release })
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	t.Cleanup(func() { cleanup.Do(func() { close(release) }) })
	f.idle(t)
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "inflight-first"})
	if text := f.waitOffer(t); !strings.Contains(text, "inflight-first") {
		t.Fatal("setup: first offer lost mail", text)
	}
	if len(collect(f.lines, 2, time.Second)) != 2 {
		t.Fatal("setup: first write never landed")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("setup: no actual receipt request")
	}
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "inflight-second"})
	if frames := collect(f.lines, 1, 300*time.Millisecond); len(frames) != 0 {
		t.Fatalf("another writer bypassed the in-flight reservation: %v", frames)
	}
	cleanup.Do(func() { close(release) })
	f.waitWritten(t)
	if text := f.waitOffer(t); !strings.Contains(text, "inflight-second") || strings.Contains(text, "inflight-first") {
		t.Fatalf("queued item lost or prior item repeated: %q", text)
	}
	if len(collect(f.lines, 2, time.Second)) != 2 {
		t.Fatal("queued item had no actual write after receipt")
	}
	f.waitWritten(t)
}
