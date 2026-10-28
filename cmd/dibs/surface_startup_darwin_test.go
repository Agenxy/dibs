// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/testexec"
)

// Hold ancestry at the actual bridge lookup boundary, not the classifier or
// an engine setter. Registration must answer while it is held. Late evidence
// repair is tested separately from the reply bound.
func TestRegisterRepliesWhileAncestryIsHeld(t *testing.T) {
	surfaceStartupContract(t, true)
}

// Also used with an independently built old bridge to measure compatibility
// across real stdio and HTTP, rather than assuming an unchanged schema works.
func TestRegisterAcrossBridgeVersions(t *testing.T) {
	surfaceStartupContract(t, false)
}

func surfaceStartupContract(t *testing.T, held bool) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "startup")
	t.Setenv("DIBS_NOTIFY", "off")
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := startupEngine(t, dir)
	// The app-shaped executable supplies actual ps evidence to both bridge and
	// daemon while never contacting the operator's app.
	app := filepath.Join(t.TempDir(), "ChatGPT.app", "Contents", "MacOS", "ChatGPT")
	if err := os.MkdirAll(filepath.Dir(app), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, self, 0o700); err != nil {
		t.Fatal(err)
	}
	testexec.PrepareExecutable(t, exec.Command(app, "--dibs-test-prepare"))
	gate := filepath.Join(t.TempDir(), "surface-gate")
	f, err := os.OpenFile(gate, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := paths.LockExclusive(f, false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mcp.New(eng))
	defer srv.Close()
	cmd := exec.Command(app, "-test.run=^TestReconnectAppFixtureHelper$")
	bridgeBinary := os.Args[0]
	if binary := os.Getenv("DIBS_TEST_SURFACE_BRIDGE_BINARY"); binary != "" {
		bridgeBinary = binary
	}
	cmd.Env = append(os.Environ(), "DIBS_TEST_RECONNECT_APP=1", "DIBS_TEST_BRIDGE_BINARY="+bridgeBinary,
		"DIBS_ADDR="+srv.URL)
	if held {
		cmd.Env = append(cmd.Env, "DIBS_TEST_SURFACE_GATE="+gate)
	}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	request := map[string]any{
		"bridge": 0,
		"rpc": map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name": "register", "arguments": map[string]any{
					"name": "late-surface", "description": "bounded ancestry fixture", "nonce": "late-surface-nonce",
					"harness": "Codex", "session_id": "late-surface-session",
				},
			},
		},
	}
	start := time.Now()
	if err := json.NewEncoder(in).Encode(request); err != nil {
		t.Fatal(err)
	}
	replies := make(chan []byte, 1)
	go func() { line, _ := bufio.NewReader(out).ReadBytes('\n'); replies <- line }()
	select {
	case line := <-replies:
		result := economyToolResult(t, line)
		if result["token"] == nil {
			t.Fatalf("registration setup failed: %v", result)
		}
		t.Logf("register replied in %s (ancestry held=%v)", time.Since(start), held)
	case <-time.After(5 * time.Second):
		if _, err := os.Stat(gate + ".entered"); err != nil {
			t.Fatalf("probe setup never entered held ancestry: %v", err)
		}
		t.Fatal("register did not reply within 5s while ancestry was held")
	}
	if !held {
		return
	}
	if _, err := os.Stat(gate + ".entered"); err != nil {
		t.Fatalf("probe setup never entered held ancestry: %v", err)
	}
	if got := startupSurface(t, eng); got != "" {
		t.Fatalf("pending ancestry was misclassified as %q", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func startupSurface(t *testing.T, eng interface {
	Board(context.Context) (core.Result, error)
}) string {
	t.Helper()
	board, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == "late-surface" {
			if info, _ := row["agent"].(*core.AgentInfo); info != nil {
				return info.Surface
			}
			return ""
		}
	}
	t.Fatal("setup: registered agent is missing")
	return ""
}
