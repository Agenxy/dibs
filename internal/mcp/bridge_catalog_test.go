// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestBridgeCatalogObservationEntersThroughMCPWithoutContact(t *testing.T) {
	for _, era := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(era, func(t *testing.T) {
			srv, e, s := newServerWithEngine(t)
			s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
				return harnessenv.AppIncarnation{}, false, nil
			}
			worker := recoveryOK(t, recoveryCall(t, srv, era, "register", map[string]any{"name": "worker", "nonce": "fixture-secret"}, nil))
			if worker["token"] == nil {
				t.Fatal("setup: registration omitted credential")
			}
			before, err := e.Board(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			post := func(pid int, hash string, rev uint64, scope string) {
				t.Helper()
				p := reconnectParams(s, pid)
				meta := p["_meta"].(map[string]any)
				meta[BridgeToolsHashMetaKey], meta[BridgeToolsRevisionMetaKey] = hash, rev
				p["arguments"] = map[string]any{"token": worker["token"]}
				if scope == "foreign" {
					meta[HostMetaKey] = "other-host"
				}
				if scope == "hostless" {
					meta[HostlessMetaKey] = true
				}
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/dibs/bridge-started", "params": p})
				req := httptest.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("MCP-Protocol-Version", era)
				if scope == "remote" {
					req.RemoteAddr = "192.0.2.1:12345"
				}
				if scope == "invited" {
					req = req.WithContext(engine.WithInvitation(context.Background(), engine.Invitation{Name: "fixture"}))
				}
				out := httptest.NewRecorder()
				s.ServeHTTP(out, req)
				if out.Code != http.StatusAccepted && scope != "invited" {
					t.Fatalf("notification refused: %d %s", out.Code, out.Body.String())
				}
			}
			for _, scope := range []string{"foreign", "hostless", "remote", "invited"} {
				post(100, "0123456789abcdef", 1, scope)
			}
			post(101, "malformed", 1, "")
			post(102, "0123456789abcdef", 2, "")
			post(102, "", 0, "") // an earlier completion must not erase newer evidence
			post(103, "", 0, "") // genuinely old bridge: unknown
			after, err := e.Board(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if after["serial"] != before["serial"] || !reflect.DeepEqual(before["agents"], after["agents"]) {
				t.Fatal("observation changed contact, authority or replayable state")
			}
			rows, ok := after["bridge_tool_catalogs"].([]engine.BridgeToolCatalog)
			if !ok || len(rows) != 2 || rows[0].PID != 102 || rows[0].Hash != "0123456789abcdef" || rows[0].Revision != 2 || len(rows[0].Agents) != 1 || rows[0].Agents[0] != "worker" || rows[1].Hash != "" || rows[0].CurrentHash != ToolsFingerprint() {
				t.Fatalf("wrong bounded catalog evidence: %v", after["bridge_tool_catalogs"])
			}
		})
	}
}
