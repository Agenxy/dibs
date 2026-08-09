package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/engine"
	"github.com/agenxy/lanes/internal/ledger"
)

//nolint:unparam // the CancelFunc is part of the constructor's contract
func newServer(t *testing.T) (*httptest.Server, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("test", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); _ = led.Close() })
	return srv, cancel
}

func rpc(t *testing.T, srv *httptest.Server, version, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	hreq, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	if version != "" {
		hreq.Header.Set("MCP-Protocol-Version", version)
	}
	resp, err := srv.Client().Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func toolCall(t *testing.T, srv *httptest.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %v", name, out)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	_ = json.Unmarshal([]byte(content), &payload)
	if isErr, _ := result["isError"].(bool); isErr {
		payload["__is_error"] = true
	}
	return payload
}

func TestDualVersionSurface(t *testing.T) {
	srv, _ := newServer(t)

	// 2026-07-28: server/discover with instructions.
	out := rpc(t, srv, "2026-07-28", "server/discover", map[string]any{})
	res := out["result"].(map[string]any)
	if res["supportedVersions"].([]any)[0] != "2026-07-28" {
		t.Fatalf("discover versions: %v", res["supportedVersions"])
	}
	if !strings.Contains(res["instructions"].(string), "register_lane") {
		t.Fatal("instructions must teach the protocol")
	}

	// Legacy path: initialize still answers for pre-2026 hosts.
	out = rpc(t, srv, "", "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	if out["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatalf("legacy initialize: %v", out)
	}

	// Unsupported version → -32022 with the supported list.
	out = rpc(t, srv, "1999-01-01", "tools/list", map[string]any{})
	rpcErr := out["error"].(map[string]any)
	if int(rpcErr["code"].(float64)) != errUnsupportedProtocolVersion {
		t.Fatalf("want -32022, got %v", rpcErr)
	}
}

func TestFullCoordinationFlow(t *testing.T) {
	srv, _ := newServer(t)

	// Register two lanes; gate; claim conflict; message round-trip.
	ra := toolCall(t, srv, "register_lane", map[string]any{"name": "alpha"})
	ta := ra["token"].(string)
	rb := toolCall(t, srv, "register_lane", map[string]any{
		"name": "rev", "kind": "persistent", "nonce": "nonce-e2e-secret",
	})
	tb := rb["token"].(string)

	// Awareness gate enforced with hint.
	slot := toolCall(t, srv, "set_slot", map[string]any{"token": ta, "text": "x"})
	if slot["code"] != "E_MUST_ACK_BOARD" || slot["hint"] == "" {
		t.Fatalf("gate: %v", slot)
	}
	toolCall(t, srv, "ack_board", map[string]any{"token": ta})
	toolCall(t, srv, "ack_board", map[string]any{"token": tb})

	// Claim matrix over HTTP.
	c1 := toolCall(t, srv, "claim", map[string]any{"token": ta, "path": "/e2e/zone", "mode": "shared"})
	if c1["granted"] != true {
		t.Fatalf("shared claim: %v", c1)
	}
	c2 := toolCall(t, srv, "claim", map[string]any{"token": tb, "path": "/e2e/zone/sub", "mode": "exclusive"})
	if c2["granted"] != false {
		t.Fatalf("exclusive over shared must refuse: %v", c2)
	}

	// Question → respond → sender reads the answer via get_message.
	q := toolCall(t, srv, "send_message", map[string]any{
		"token": ta, "to": "rev", "type": "question", "body": "ready?", "op_id": "q1",
	})
	serial := q["msg_serial"].(float64)
	// Duplicate send dedups.
	q2 := toolCall(t, srv, "send_message", map[string]any{
		"token": ta, "to": "rev", "type": "question", "body": "ready?", "op_id": "q1",
	})
	if q2["deduplicated"] != true {
		t.Fatalf("dedup: %v", q2)
	}
	toolCall(t, srv, "respond", map[string]any{
		"token": tb, "msg_serial": serial, "disposition": "answer", "body": "yes, ship it",
	})
	got := toolCall(t, srv, "get_message", map[string]any{"token": ta, "msg_serial": serial})
	msg := got["message"].(map[string]any)
	if msg["response"] != "yes, ship it" || msg["state"] != "answered" {
		t.Fatalf("sender must read the answer: %v", msg)
	}

	// events_since carries metadata, never bodies.
	evs := toolCall(t, srv, "events_since", map[string]any{"token": ta, "since_serial": 0})
	raw, _ := json.Marshal(evs)
	if strings.Contains(string(raw), "yes, ship it") || strings.Contains(string(raw), "ready?") {
		t.Fatal("bodies must never ride events")
	}
}
