// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSmokeUsesSelectedEndpointForInstalledCLI(t *testing.T) {
	r, cli := commandTestFixture(t, "dibs")
	daemon := filepath.Join(r.root, "dibd")
	if err := copyCandidateFile(cli, daemon); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(daemon, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	r.ctx = ctx
	home := filepath.Join(r.root, "home")
	env := []string{"DIBS_ADDR=127.0.0.1:1", "DIBS_TEST_INSTALLED_MODE=smoke"}
	if err := r.smoke(r.root, home, env); err != nil {
		t.Fatalf("actual smoke did not give its installed CLI the fixture daemon's endpoint: %v", err)
	}
	if r.report.MCPRoundTrips != 2 {
		t.Fatalf("smoke did not finish both installed bridge protocols: %+v", r.report)
	}
}

func installedDaemonFixture() int {
	var addr string
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "-addr" {
			addr = os.Args[i+1]
		}
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") || addr == "127.0.0.1:1" {
		return 10
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 11
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "private-smoke-fixture")
	})}
	_ = server.Serve(ln)
	return 12
}

func smokeEndpointObserved() bool {
	addr := os.Getenv("DIBS_ADDR")
	if !strings.HasPrefix(addr, "127.0.0.1:") || addr == "127.0.0.1:1" {
		return false
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + addr)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return err == nil && string(body) == "private-smoke-fixture"
}

func TestInstalledBoardUsesContainedCLIAndRejectsBadOutput(t *testing.T) {
	for _, mode := range []string{"ok", "malformed", "refused", "missing-node"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := commandTestFixture(t, "dibs")
			board, err := r.installedBoard(r.ctx, r.root, []string{"DIBS_TEST_INSTALLED_MODE=" + mode})
			if (err != nil) != (mode != "ok") {
				t.Fatalf("wrong CLI result: %+v %v", board, err)
			}
			if mode == "ok" && (board.Node != "original-board" || board.Serial != 437) {
				t.Fatalf("actual CLI continuity evidence was lost: %+v", board)
			}
		})
	}
}

func TestInstalledBridgeChecksProtocolsRepliesAndPrivateLogs(t *testing.T) {
	for _, protocol := range []string{"2026-07-28", "2025-11-25"} {
		for _, mode := range []string{"ok", "wrong-id", "rpc-refused", "tool-refused", "wrong-protocol"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				r, _ := commandTestFixture(t, "dibs")
				env := []string{"DIBS_TEST_INSTALLED_MODE=" + mode, "DIBS_TEST_INSTALLED_PROTOCOL=" + protocol}
				err := r.installedBridge(r.root, r.root, env, protocol, 0)
				if (err != nil) != (mode != "ok") {
					t.Fatalf("wrong bridge result: %v", err)
				}
				if len(r.report.Steps) != 1 || (r.report.Steps[0].Exit == 0) != (mode == "ok") {
					t.Fatalf("stdio proof failure disappeared from receipt: %+v", r.report.Steps)
				}
				log, err := os.ReadFile(filepath.Join(r.root, r.report.Steps[0].Log))
				if err != nil || strings.Contains(string(log), "private-fixture-token") || !strings.Contains(string(log), protocol) {
					t.Fatalf("private RPC body leaked or protocol log lost: %v %s", err, log)
				}
			})
		}
	}
}

func installedCommandFixture() int {
	mode := os.Getenv("DIBS_TEST_INSTALLED_MODE")
	if mode == "smoke" && !smokeEndpointObserved() {
		return 20
	}
	if len(os.Args) == 3 && os.Args[1] == "board" && os.Args[2] == "--json" {
		switch mode {
		case "malformed":
			fmt.Print("{")
		case "refused":
			return 1
		case "missing-node":
			fmt.Print(`{"serial":437}`)
		default:
			fmt.Print(`{"node":"original-board","serial":437}`)
		}
		return 0
	}
	if len(os.Args) != 2 || os.Args[1] != "mcp-stdio" {
		return 2
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     int
			Method string
			Params map[string]any
		}
		if err := decoder.Decode(&req); err == io.EOF {
			return 0
		} else if err != nil {
			return 3
		}
		if req.ID == 0 {
			continue
		}
		meta, ok := req.Params["_meta"].(map[string]any)
		if mode == "smoke" && ok {
			protocol, _ := meta["io.modelcontextprotocol/protocolVersion"].(string)
			if protocol != "2026-07-28" && protocol != "2025-11-25" {
				return 21
			}
			if err := os.Setenv("DIBS_TEST_INSTALLED_PROTOCOL", protocol); err != nil {
				return 22
			}
		}
		if !ok || meta["io.modelcontextprotocol/protocolVersion"] != os.Getenv("DIBS_TEST_INSTALLED_PROTOCOL") {
			return 4
		}
		id := req.ID
		if mode == "wrong-id" {
			id++
		}
		result := installedFixtureResult(req.Method, req.Params, mode)
		reply := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		if mode == "rpc-refused" {
			reply = map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000}}
		}
		if err := encoder.Encode(reply); err != nil {
			return 5
		}
	}
}

func installedFixtureResult(method string, params map[string]any, mode string) map[string]any {
	protocol := os.Getenv("DIBS_TEST_INSTALLED_PROTOCOL")
	if mode == "wrong-protocol" {
		protocol = "unsupported-fixture"
	}
	if method == "server/discover" {
		return map[string]any{"supportedVersions": []string{protocol}}
	}
	if method == "initialize" {
		return map[string]any{"protocolVersion": protocol}
	}
	var body any = map[string]any{"ok": true}
	switch params["name"] {
	case "register":
		body = map[string]any{"token": "private-fixture-token"}
	case "declare":
		body = map[string]any{"slot_id": "s1", "overlaps": []any{
			map[string]any{"agent": "surface-a-0"}, map[string]any{"agent": "surface-a-1"},
		}}
	case "send":
		body = map[string]any{"msg_serial": 7}
	case "inbox":
		body = map[string]any{"body": "Are you checking this installation?"}
	case "read_mail":
		body = map[string]any{"response": "Yes, installation checked."}
	}
	encoded, _ := json.Marshal(body)
	return map[string]any{"isError": mode == "tool-refused", "content": []any{map[string]any{
		"type": "text", "text": string(encoded),
	}}}
}
