// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/mcp"
)

// Start the actual stdio loop. This catches a catalogOnReply setter/test that
// works in isolation while its production caller is absent.
func TestRealBridgeReportsServedCatalogWithoutAnotherHarnessCall(t *testing.T) {
	f := newRecoveryStdio(t)
	var reply struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(f.exchange("tools/list", map[string]any{}), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Tools) == 0 {
		t.Fatal("setup: actual bridge returned no catalog")
	}
	want := mcp.FingerprintTools(reply.Result.Tools)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		board, err := f.eng.Board(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := board["bridge_tool_catalogs"].([]engine.BridgeToolCatalog)
		for _, row := range rows {
			if row.Hash == want && row.Revision == 1 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual stdio loop never reported its served catalog without another harness call")
}

type failedCatalogWriter struct{}

func (failedCatalogWriter) Write([]byte) (int, error) { return 0, errors.New("closed harness") }

func TestBridgeCatalogComesFromActualCompleteServedReply(t *testing.T) {
	hash, revision := catalogServed()
	t.Cleanup(func() { restoreCatalog(hash, revision) })
	restoreCatalog("", 0)
	changed := make(chan struct{}, 1)
	callback := catalogOnReply(changed, func([]byte, []byte) {})
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	reply := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"older-daemon-tool","inputSchema":{"type":"object"}}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(reply)) }))
	defer server.Close()
	var output bytes.Buffer
	forwardReply := func(out *syncWriter) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(request))
		if err != nil {
			t.Fatal(err)
		}
		forward(server.Client(), req, request, out, callback)
	}
	forwardReply(&syncWriter{w: bufio.NewWriter(failedCatalogWriter{})})
	if got, _ := catalogServed(); got != "" {
		t.Fatal("failed stdout write advertised a served catalog")
	}
	forwardReply(&syncWriter{w: bufio.NewWriter(&output)})
	if output.String() != reply+"\n" {
		t.Fatal("setup: harness did not receive exact list")
	}
	var body struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply), &body); err != nil {
		t.Fatal(err)
	}
	want := mcp.FingerprintTools(body.Result.Tools)
	if got, rev := catalogServed(); got != want || rev != 1 || got == mcp.ToolsFingerprint() {
		t.Fatalf("binary replaced actual served evidence: %s/%d want%s", got, rev, want)
	}
	carried := handoffState()
	if carried.ServedToolsHash != want || carried.ServedToolsRevision != 1 {
		t.Fatal("handoff discarded actual served evidence")
	}
	for _, legacy := range []bool{false, true} {
		state := bridgeState{ToolsHash: carried.ToolsHash, ServedToolsHash: carried.ServedToolsHash, ServedToolsRevision: carried.ServedToolsRevision}
		if legacy {
			state.ServedToolsHash, state.ServedToolsRevision = "", 0
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(bridgeStateEnv, string(encoded))
		restoreCatalog("unrelated-binary", 99)
		ctx, cancel := context.WithCancel(t.Context())
		var streams sync.WaitGroup
		restoreCarried(ctx, server.Client(), server.URL, "secret", &syncWriter{w: bufio.NewWriter(&output)}, &streams, nil, false, shipTiming{})
		cancel()
		got, rev := catalogServed()
		if got != state.ServedToolsHash || rev != state.ServedToolsRevision {
			t.Fatalf("restore substituted binary fingerprint or lost served evidence: %q/%d", got, rev)
		}
	}
	restoreCatalog(want, 1)
	// Inspect the actual metadata enrichment production uses, not a manually
	// constructed notification that could pass with the client wire missing.
	if started := bridgeStarted(); started != "" {
		line := enrichBridgeProcess(request)
		var enriched struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &enriched); err != nil {
			t.Fatal(err)
		}
		if enriched.Params.Meta[mcp.BridgeToolsHashMetaKey] != want || enriched.Params.Meta[mcp.BridgeToolsRevisionMetaKey] != float64(1) {
			t.Fatalf("served catalog not wired into requests: %s", line)
		}
	}
	for _, invalid := range []string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"refused"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"more"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
	} {
		callback(request, []byte(invalid))
		if got, rev := catalogServed(); got != want || rev != 1 {
			t.Fatal("incomplete/error reply changed catalog evidence")
		}
	}
	select {
	case <-changed:
	default:
		t.Fatal("no event armed metadata completion")
	}
}
