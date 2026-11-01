// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/logs"
	"github.com/agenxy/dibs/internal/mcp"
)

// Enter through a real forwarded registration and its production callback.
// The lookup seam supplies a completed observation; no setter arms the worker.
func TestSurfaceCompletionDebugExplainsOmissionAndAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, surface, result, message string
		changed                        any
		posts                          int32
	}{
		{"no-app", "", `{"ok":true}`, "late bridge surface completion omitted: no recognized app or Codex fallback", nil, 0},
		{"explicit-no-change", "chatgpt-app", `{"ok":true,"changed":false}`, "late bridge surface evidence acknowledged by daemon", false, 1},
		{"unreported-change", "chatgpt-app", `{"ok":true}`, "late bridge surface evidence acknowledged by daemon", "not_reported", 1},
		{"opaque-change", "chatgpt-app", `{"ok":true,"changed":"future metadata"}`, "late bridge surface evidence acknowledged by daemon", "not_reported", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousLogger := slog.Default()
			previousProbe, previousDetect := bridgeAncestry, detectedSurface
			previousRemote, previousClient := remoteSession, lastClientInfo
			bridgeAncestry = &surfaceProbe{}
			detectedSurface = func(context.Context) (string, error) { return tc.surface, nil }
			remoteSession, lastClientInfo = false, nil
			ring := logs.NewRing(16)
			slog.SetDefault(slog.New(logs.NewHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), ring)))
			ctx, cancel := context.WithCancel(t.Context())
			var workers sync.WaitGroup
			defer func() {
				cancel()
				workers.Wait()
				slog.SetDefault(previousLogger)
				bridgeAncestry, detectedSurface = previousProbe, previousDetect
				remoteSession, lastClientInfo = previousRemote, previousClient
			}()
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Method string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if request.Method == "tools/call" {
					_, _ = w.Write(surfaceRegistrationReply(t, "own-row", "own-token", true))
					return
				}
				if request.Method != mcp.BridgeSurfaceEvidenceMethod {
					t.Errorf("unexpected method %q", request.Method)
					return
				}
				posts.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "dibs-surface-evidence", "result": map[string]any{
					"content": []map[string]string{{"type": "text", "text": tc.result}},
				}})
			}))
			defer srv.Close()
			callback := surfaceOnRegister(ctx, srv.Client(), srv.URL, "fixture-secret", &workers, func([]byte, []byte) {})
			line := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{}}}`)
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			forward(srv.Client(), req, line, &syncWriter{w: bufio.NewWriter(&output)}, callback)
			if agentTokenIn(bytes.TrimSpace(output.Bytes())) != "own-token" {
				t.Fatal("setup: forwarded registration did not return its own credential")
			}
			deadline := time.Now().Add(time.Second)
			for {
				for _, record := range ring.Tail(0) {
					if record.Msg == tc.message && record.Level == "DEBUG" {
						if posts.Load() != tc.posts || (tc.changed != nil && record.Attrs["changed"] != tc.changed) {
							t.Fatalf("wrong completion evidence: posts=%d attrs=%v", posts.Load(), record.Attrs)
						}
						return
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("silent surface completion: missing Debug evidence %q", tc.message)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
