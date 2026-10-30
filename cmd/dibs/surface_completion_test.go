// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

func surfaceRegistrationReply(t *testing.T, id, token string, capable bool) []byte {
	t.Helper()
	text, err := json.Marshal(map[string]any{"agent_id": id, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
		"_meta":   map[string]any{mcp.BridgeSurfaceEvidenceMetaKey: capable},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSurfaceCompletionBoundsOwnedRepliesAndReplacesRotatedTokens(t *testing.T) {
	c := &surfaceCompletion{pending: map[string]surfaceRepair{}}
	sent := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{}}}`)
	for i := range maxSurfaceRepairs + 1 {
		c.remember(sent, surfaceRegistrationReply(t, fmt.Sprintf("worker-%d", i), "old-token", true))
	}
	if len(c.pending) != maxSurfaceRepairs {
		t.Fatalf("pending repair bound lost: %d", len(c.pending))
	}
	c.remember(sent, surfaceRegistrationReply(t, "worker-0", "rotated-token", true))
	if c.pending["worker-0"].token != "rotated-token" || len(c.pending) != maxSurfaceRepairs {
		t.Fatal("token rotation was dropped at the bound")
	}
	for _, name := range []string{"check_in", "update", "send"} {
		line := []byte(`{"method":"tools/call","params":{"name":"` + name + `"}}`)
		c.remember(line, surfaceRegistrationReply(t, "worker-0", "unowned-token", true))
	}
	if c.pending["worker-0"].token != "rotated-token" {
		t.Fatal("an unrelated tool reply selected a registration credential")
	}
	unsupported := &surfaceCompletion{pending: map[string]surfaceRepair{}}
	unsupported.remember(sent, surfaceRegistrationReply(t, "old-board", "token", false))
	if len(unsupported.pending) != 0 {
		t.Fatal("old daemon reply armed late surface repair")
	}
}

// Obtain a capability through the actual forward callback, then replace the
// handler's method table with an old daemon's refusal at the SAME address.
// No ordinary update may run, and no failure is retried until the normal
// bridge route explicitly re-resolves, even if the address string is unchanged.
func TestSurfaceCompletionFailsClosedAcrossAnAdvertisedDaemonDowngrade(t *testing.T) {
	var downgraded atomic.Bool
	var calls, ordinaryUpdates atomic.Int32
	reply := surfaceRegistrationReply(t, "owned-worker", "owned-token", true)
	dir := cliIPCProfile(t)
	srv := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				Name  string `json:"name"`
				Token string `json:"token"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "tools/call" && request.Params.Name == "register" {
			_, _ = w.Write(reply)
			return
		}
		if request.Method == "tools/call" && request.Params.Name == "update" {
			ordinaryUpdates.Add(1)
		}
		if request.Method != mcp.BridgeSurfaceEvidenceMethod || request.Params.Token != "owned-token" {
			t.Error("completion used another route or credential")
		}
		calls.Add(1)
		if downgraded.Load() {
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":"dibs-surface-evidence","error":{"code":-32601,"message":"method not found"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":"dibs-surface-evidence","result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)
	}))
	defer srv.Close()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", srv.URL)
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), bytes.Repeat([]byte("a"), 64), 0o600); err != nil {
		t.Fatal(err)
	}
	route, err := newBridgeRoute()
	if err != nil {
		t.Fatal(err)
	}
	client := bridgeDaemonClient(route, time.Second)
	c := &surfaceCompletion{pending: map[string]surfaceRepair{}, probe: &surfaceProbe{known: true, surface: "chatgpt-app"}}
	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{}}}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	out := &syncWriter{w: bufio.NewWriter(&output)}
	forward(client, req, line, out, c.remember)
	out.flush()
	if len(c.pending) != 1 || agentTokenIn(bytes.TrimSpace(output.Bytes())) != "owned-token" {
		t.Fatal("setup: actual registration reply/callback did not arm its own repair")
	}
	downgraded.Store(true)
	c.advance(context.Background(), client, srv.URL, "placeholder")
	if calls.Load() != 1 || ordinaryUpdates.Load() != 0 || len(c.pending) != 1 || !c.unsupported {
		t.Fatal("old daemon refusal fell back, retried, or lost the pending evidence")
	}
	downgraded.Store(false)
	c.advance(context.Background(), client, srv.URL, "placeholder")
	if calls.Load() != 1 {
		t.Fatal("failed RPC retried without address re-resolution")
	}
	if _, _, err := route.current(true); err != nil {
		t.Fatal(err)
	}
	c.advance(context.Background(), client, srv.URL, "placeholder")
	if calls.Load() != 2 || ordinaryUpdates.Load() != 0 || len(c.pending) != 0 {
		t.Fatal("normal address re-resolution did not permit one safe repair")
	}
}
