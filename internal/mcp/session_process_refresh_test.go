// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/localtransport"
	"github.com/agenxy/dibs/internal/procinfo"
)

type nativeSessionProber struct{}

func (nativeSessionProber) Alive(pid int) bool {
	_, err := procinfo.Start(pid)
	return err == nil
}

func (nativeSessionProber) StartMillis(pid int) (int64, bool) {
	start, err := procinfo.Start(pid)
	return start.UnixMilli(), err == nil
}

func TestAuthenticatedContactRepairsAnUnboundLiveClaudeSession(t *testing.T) {
	for _, method := range []string{"inbox", "check_in"} {
		t.Run(method, func(t *testing.T) { testClaudeProcessRepair(t, method) })
	}
}

func testClaudeProcessRepair(t *testing.T, method string) {
	t.Helper()
	const primary = "7d767ad4-1b2c-43bf-8e0c-112233445566"
	const oldCurrent = "72fc2cf9-1b2c-43bf-8e0c-112233445566"
	const current = "37b575e3-761f-43af-850e-ffce61781980"
	const deadPID = 99999999
	parent, bridge := os.Getppid(), os.Getpid()
	parentStart, err := procinfo.Start(parent)
	if err != nil {
		t.Fatal("setup: parent does not have a native incarnation", err)
	}
	bridgeStart, err := procinfo.Start(bridge)
	if err != nil || (nativeSessionProber{}).Alive(deadPID) {
		t.Fatal("setup: missing bridge or unexpectedly live old process", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	// Deliberately no peer key or wake socket. The live process already
	// advertises its session through the sidecar used by its direct bridge.
	sidecar, err := json.Marshal(map[string]any{
		"sessionId": current, "pid": parent, "procStart": procinfo.Stamp(parentStart),
	})
	if err != nil {
		t.Fatal(err)
	}
	sidecarPath := filepath.Join(sessions, strconv.Itoa(parent)+".json")
	if err := os.WriteFile(sidecarPath, sidecar, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	eng, replay := sessionRefreshEngine(t)
	reg, err := eng.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", PID: deadPID,
		SessionID: primary, SessionAlias: oldCurrent, Agent: &core.AgentInfo{Harness: "Claude Code"},
	})
	if err != nil {
		t.Fatal("setup: register", err)
	}
	token, _ := reg["token"].(string)
	if token == "" {
		t.Fatal("setup: missing worker token")
	}
	srv := localSessionServer(t, eng)
	assertBinding := func(id string, pid int, start int64) {
		t.Helper()
		folded := replay()
		a := folded.Agents["worker"]
		if a.SessionID != primary || a.CurrentSession != id || a.PID != pid || a.ProcStart != start {
			t.Fatalf("replayed binding primary=%s current=%s pid=%d start=%d, want %s %s %d %d",
				a.SessionID, a.CurrentSession, a.PID, a.ProcStart, primary, id, pid, start)
		}
	}
	assertBinding(oldCurrent, deadPID, 0)
	meta := map[string]any{SessionMetaKey: current, BridgePIDMetaKey: bridge, BridgeStartMetaKey: procinfo.Stamp(bridgeStart)}
	readInbox := func(server *httptest.Server, evidence map[string]any) {
		t.Helper()
		if res := toolCallWithMeta(t, server, "inbox", map[string]any{"token": token}, evidence); res["inbox"] == nil {
			t.Fatalf("authenticated inbox failed: %v", res)
		}
	}
	if res := toolCallWithMeta(t, srv, "inbox", map[string]any{"token": "wrong-token"}, meta); res["code"] != "E_BAD_TOKEN" {
		t.Fatalf("invalid credential reached the inbox: %v", res)
	}
	assertBinding(oldCurrent, deadPID, 0)
	remote := httptest.NewServer(New(eng))
	t.Cleanup(remote.Close)
	readInbox(remote, meta)
	assertBinding(oldCurrent, deadPID, 0)
	// A build without the verified birth stamp must be able to try again on
	// its next call. The incomplete observation cannot poison ambient adoption.
	if err := os.WriteFile(sidecarPath, []byte(`{"sessionId":"`+current+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	readInbox(srv, meta)
	assertBinding(oldCurrent, deadPID, 0)
	if err := os.WriteFile(sidecarPath, sidecar, 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "holder", SessionID: current})
	if err != nil {
		t.Fatal("setup: register existing holder", err)
	}
	readInbox(srv, meta)
	assertBinding(oldCurrent, deadPID, 0)
	if _, err := eng.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: holder["token"].(string), ReleaseSession: true}); err != nil {
		t.Fatal("setup: release held session", err)
	}
	res := toolCallWithMeta(t, srv, method, map[string]any{"token": token}, meta)
	if res["inbox"] == nil {
		t.Fatalf("authenticated %s failed: %v", method, res)
	}
	assertBinding(current, parent, parentStart.UnixMilli())
	board, err := eng.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == "worker" && row["proc_alive"] != true {
			t.Fatalf("live replacement process is missing from the board projection: %v", row)
		}
	}
	readInbox(srv, meta)
	assertBinding(current, parent, parentStart.UnixMilli())
}

func sessionRefreshEngine(t *testing.T) (*engine.Engine, func() *core.State) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	led, err := ledger.Open(ledgerPath, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("test", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, led, nativeSessionProber{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = led.Close() })
	return eng, func() *core.State {
		t.Helper()
		reader, err := ledger.OpenReadOnly(ledgerPath, "test", box)
		if err != nil {
			t.Fatal("open replay reader", err)
		}
		defer func() { _ = reader.Close() }()
		folded := core.NewState("test", core.DefaultLimits())
		if _, err := reader.Replay(folded); err != nil {
			t.Fatal("replay", err)
		}
		return folded
	}
}

func localSessionServer(t *testing.T, eng *engine.Engine) *httptest.Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "dibs-uds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := localtransport.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(New(eng))
	_ = srv.Listener.Close()
	srv.Listener = listener
	srv.Config.ConnContext = localtransport.ConnContext
	srv.Start()
	t.Cleanup(srv.Close)
	srv.URL = "http://localhost"
	srv.Client().Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localtransport.Dial(ctx, dir)
	}}
	return srv
}
