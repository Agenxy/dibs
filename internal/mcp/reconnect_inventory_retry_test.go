// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestTwoFailedInventoriesStayVisibleUntilAnyLocalCall(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	var scans atomic.Int32
	s.bridges.batchProbe = func(_ context.Context, bridges []harnessenv.BridgeIncarnation) ([]harnessenv.BridgeObservation, error) {
		if scans.Add(1) <= 2 {
			return nil, os.ErrDeadlineExceeded
		}
		out := make([]harnessenv.BridgeObservation, len(bridges))
		for i := range out {
			out[i].Present = true
		}
		return out, nil
	}
	before, err := eng.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result(t, rpc(t, srv, "2026-07-28", "server/discover", reconnectParams(s, 8100)), "two failed observations")
	board, err := eng.Board(t.Context())
	advice, _ := board["reconnect_advice"].(string)
	if err != nil || scans.Load() != 2 || board["reconnect_pending"] != 1 ||
		!strings.Contains(advice, "any agent") || board["serial"] != before["serial"] {
		t.Fatalf("two failures were retried unboundedly, hidden, or sent mail: scans=%d board=%v err=%v", scans.Load(), board, err)
	}
	// No metadata from the original bridge: an unrelated local call suffices.
	result(t, rpc(t, srv, "2026-07-28", "server/discover", map[string]any{}), "another local agent")
	board, err = eng.Board(t.Context())
	if err != nil || scans.Load() != 3 || board["reconnect_pending"] != nil || board["serial"] != before["serial"] {
		t.Fatalf("next local call failed to recover silently: scans=%d board=%v err=%v", scans.Load(), board, err)
	}
}

func TestImmediateInventoryRetryCancelsWithTheEngine(t *testing.T) {
	srv, cancelEngine := newServer(t)
	s := srv.Config.Handler.(*Server)
	entered := make(chan struct{})
	var scans atomic.Int32
	s.bridges.batchProbe = func(ctx context.Context, _ []harnessenv.BridgeIncarnation) ([]harnessenv.BridgeObservation, error) {
		if scans.Add(1) == 1 {
			return nil, os.ErrDeadlineExceeded
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/dibs/bridge-started", "params": reconnectParams(s, 8200),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		s.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		cancelEngine()
		<-done
		t.Fatal("failed first inventory did not enter its one immediate retry")
	}
	cancelEngine()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("immediate retry outlived engine shutdown")
	}
	if scans.Load() != 2 {
		t.Fatalf("retry count=%d, want2", scans.Load())
	}
}
