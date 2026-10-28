// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func reconnectParams(s *Server, pid int) map[string]any {
	return map[string]any{"_meta": map[string]any{
		BridgePIDMetaKey: pid, BridgeStartMetaKey: "Sun Oct 4 06:00:00 2026", HostMetaKey: s.eng.HostID(),
	}}
}

func TestReconnectRetryBatchIsBoundedAndGivesUpOnlyOnce(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	_, humanToken, err := eng.HumanAgent(t.Context())
	if err != nil {
		t.Fatal("setup:", err)
	}
	var scans, largest int
	ready := false
	s.bridges.batchProbe = func(_ context.Context, bridges []harnessenv.BridgeIncarnation) ([]harnessenv.BridgeObservation, error) {
		scans++
		if len(bridges) > largest {
			largest = len(bridges)
		}
		if len(bridges) > maxObservedBridges {
			t.Fatalf("unbounded batch: %d", len(bridges))
		}
		if !ready {
			return nil, os.ErrDeadlineExceeded
		}
		out := make([]harnessenv.BridgeObservation, len(bridges))
		for i := range out {
			out[i].Present = true
		}
		return out, nil
	}
	for i := range maxObservedBridges + 2 {
		before := scans
		result(t, rpc(t, srv, "2026-07-28", "server/discover", reconnectParams(s, 1000+i)), "bounded discovery")
		if scans != before+2 {
			t.Fatalf("one failing event must take one shared inventory plus one bounded retry, got %d", scans-before)
		}
	}
	if largest != maxObservedBridges {
		t.Fatalf("setup: only reached %d pending entries", largest)
	}
	board, err := eng.Board(t.Context())
	if err != nil || board["reconnect_pending"] != maxObservedBridges {
		t.Fatalf("bound/diagnostic: %v %v", board, err)
	}
	inbox, err := eng.Inbox(t.Context(), humanToken)
	if err != nil {
		t.Fatal(err)
	}
	messages := inbox["messages"].([]*core.Message)
	if len(messages) != 1 || messages[0].From != "dibs" {
		t.Fatalf("eviction must send one deduplicated fault, got %v", messages)
	}
	ready = true
	before := scans
	result(t, rpc(t, srv, "2026-07-28", "server/discover", map[string]any{}), "retry entire pending batch")
	if scans != before+1 {
		t.Fatalf("recovery took %d inventories", scans-before)
	}
	board, err = eng.Board(t.Context())
	if err != nil || board["reconnect_pending"] != nil {
		t.Fatalf("recovery did not clear pending diagnostic: %v %v", board, err)
	}
	inbox, err = eng.Inbox(t.Context(), humanToken)
	if err != nil || len(inbox["messages"].([]*core.Message)) != 1 {
		t.Fatalf("recovery sent extra mail: %v %v", inbox, err)
	}
}

func TestReconnectRetriesStopWithTheEngine(t *testing.T) {
	srv, cancelEngine := newServer(t)
	s := srv.Config.Handler.(*Server)
	entered := make(chan struct{})
	s.bridges.batchProbe = func(ctx context.Context, _ []harnessenv.BridgeIncarnation) ([]harnessenv.BridgeObservation, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/dibs/bridge-started", "params": reconnectParams(s, 2000)})
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
		t.Fatal("setup: observer never entered")
	}
	cancelEngine()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("observation outlived the daemon")
	}
}

func TestPendingReconnectReportsGiveUpWhenItsIncarnationIsGone(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	_, token, err := eng.HumanAgent(t.Context())
	if err != nil {
		t.Fatal("setup:", err)
	}
	var scans int
	s.bridges.batchProbe = func(_ context.Context, bridges []harnessenv.BridgeIncarnation) ([]harnessenv.BridgeObservation, error) {
		scans++
		if scans <= 2 {
			return nil, os.ErrDeadlineExceeded
		}
		return make([]harnessenv.BridgeObservation, len(bridges)), nil // PID/start no longer match
	}
	result(t, rpc(t, srv, "2026-07-28", "server/discover", reconnectParams(s, 5000)), "failed observation")
	result(t, rpc(t, srv, "2026-07-28", "server/discover", map[string]any{}), "bridge departed")
	inbox, err := eng.Inbox(t.Context(), token)
	if err != nil || len(inbox["messages"].([]*core.Message)) != 1 {
		t.Fatalf("departed pending bridge was silently dropped: %v %v", inbox, err)
	}
	board, err := eng.Board(t.Context())
	if err != nil || board["reconnect_pending"] != nil {
		t.Fatalf("departed incarnation left a stale pending diagnostic: %v %v", board, err)
	}
}

func TestBridgeCompletionHasNoAgentBindingAuthority(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	worker := toolCall(t, srv, "register", map[string]any{"name": "fixed-worker", "nonce": "fixed-worker-nonce", "session_id": "fixed-session"})
	if worker["token"] == nil {
		t.Fatalf("setup: %v", worker)
	}
	before, err := eng.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
		probes.Add(1)
		return harnessenv.AppIncarnation{PID: 42, Start: "Sun Oct 4 05:00:00 2026"}, true, nil
	}
	for _, tc := range []struct {
		name                       string
		foreign, hostless, invited bool
		addr                       string
	}{
		{name: "peer host", foreign: true, addr: "127.0.0.1:12345"},
		{name: "no host", hostless: true, addr: "127.0.0.1:12345"},
		{name: "invitation", invited: true, addr: "127.0.0.1:12345"},
		{name: "remote transport", addr: "192.0.2.1:12345"},
		{name: "local cohort observation", addr: "127.0.0.1:12345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := reconnectParams(s, 3000+int(probes.Load()))
			meta := p["_meta"].(map[string]any)
			if tc.foreign {
				meta[HostMetaKey] = "other-host"
			}
			if tc.hostless {
				meta[HostlessMetaKey] = true
			}
			// Even malicious identity-shaped payload on an observation cannot
			// attach process/session metadata to this registered peer.
			p["arguments"] = map[string]any{"token": worker["token"], "session_id": "attacker-session", "pid": 99999, "host": "foreign"}
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/dibs/bridge-started", "params": p})
			req := httptest.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
			req.RemoteAddr = tc.addr
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
			if tc.invited {
				req = req.WithContext(engine.WithInvitation(req.Context(), engine.Invitation{Name: "fixture"}))
			}
			out := httptest.NewRecorder()
			s.ServeHTTP(out, req)
			if tc.invited {
				if out.Code != http.StatusOK || !bytes.Contains(out.Body.Bytes(), []byte("pull-only")) {
					t.Fatalf("invitation admission was not refused: %d %s", out.Code, out.Body.String())
				}
			} else if out.Code != http.StatusAccepted {
				t.Fatalf("notification: HTTP%d %s", out.Code, out.Body.String())
			}
		})
	}
	if probes.Load() != 1 {
		t.Fatalf("nonlocal or invited metadata reached local process inventory: %d probes", probes.Load())
	}
	after, err := eng.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before["serial"] != after["serial"] || !reflect.DeepEqual(before["agents"], after["agents"]) {
		t.Fatalf("observation changed a peer binding: %v -> %v", before, after)
	}
}

func TestOldBridgeRequestsStillObserveWithoutCompletionNotification(t *testing.T) {
	srv, _, s := newServerWithEngine(t)
	var probes atomic.Int32
	s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
		probes.Add(1)
		return harnessenv.AppIncarnation{}, false, nil
	}
	for _, era := range []string{"2026-07-28", "2025-11-25"} {
		method := "server/discover"
		p := reconnectParams(s, 4000+int(probes.Load()))
		if era == "2025-11-25" {
			method = "initialize"
			p["protocolVersion"] = era
		}
		result(t, rpc(t, srv, era, method, p), fmt.Sprintf("old bridge %s", era))
	}
	if probes.Load() != 2 {
		t.Fatalf("old bridge without completion lost discovery observation: %d", probes.Load())
	}
}

// An unrelated local event must retry the failed observation even when its
// original bridge has gone idle. No second request from that model is needed.
func TestUnrelatedLocalEventRetriesPendingReconnect(t *testing.T) {
	srv, _, s := newServerWithEngine(t)
	var probes atomic.Int32
	s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
		if probes.Add(1) <= 2 {
			return harnessenv.AppIncarnation{}, false, os.ErrDeadlineExceeded
		}
		return harnessenv.AppIncarnation{}, false, nil
	}
	result(t, rpc(t, srv, "2026-07-28", "server/discover", reconnectParams(s, 104)), "setup discovery")
	result(t, rpc(t, srv, "2026-07-28", "server/discover", map[string]any{}), "later local event")
	if got := probes.Load(); got != 3 {
		t.Fatalf("later local event lost a pending bridge observation after two failed attempts: probes=%d, want3", got)
	}
}

func TestPendingReconnectDiagnosticAppearsAndClearsWithoutMail(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	if _, _, err := eng.HumanAgent(t.Context()); err != nil {
		t.Fatal("setup human:", err)
	}
	var probes atomic.Int32
	s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
		if probes.Add(1) <= 2 {
			return harnessenv.AppIncarnation{}, false, os.ErrDeadlineExceeded
		}
		return harnessenv.AppIncarnation{}, false, nil
	}
	before, err := eng.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result(t, rpc(t, srv, "2026-07-28", "server/discover", reconnectParams(s, 105)), "setup discovery")
	board, err := eng.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if board["reconnect_pending"] != 1 || board["reconnect_advice"] == nil {
		t.Fatalf("failed observation is invisible on the board: %v", board)
	}
	result(t, rpc(t, srv, "2026-07-28", "server/discover", map[string]any{}), "retry event")
	board, err = eng.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if board["reconnect_pending"] != nil || board["reconnect_advice"] != nil {
		t.Fatalf("recovered observation left a stale diagnostic: %v", board)
	}
	if board["serial"] != before["serial"] {
		t.Fatalf("transient observation recovery sent fault mail or changed coordination state: %v -> %v", before["serial"], board["serial"])
	}
}

// Use ServeHTTP, including transport and metadata admission. Cancel the HTTP
// request while its process observation is held, then finish that observation.
// A second discovery distinguishes completed recovery from silently losing it.
func TestAdmittedReconnectOutlivesItsHTTPRequest(t *testing.T) {
	srv, _, s := newServerWithEngine(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
		if probes.Add(1) == 1 {
			close(entered)
			<-release
		}
		return harnessenv.AppIncarnation{PID: 42, Start: "Sun Oct 4 05:00:00 2026"}, true, nil
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "server/discover", "params": reconnectParams(s, 106)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	done := make(chan struct{})
	go func() { s.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup: request did not reach process observation")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("released observation did not finish")
	}
	result(t, rpc(t, srv, "2026-07-28", "server/discover", reconnectParams(s, 106)), "later discovery")
	if n := probes.Load(); n != 1 {
		t.Fatalf("HTTP cancellation discarded admitted recovery: probes=%d, want1", n)
	}
}
