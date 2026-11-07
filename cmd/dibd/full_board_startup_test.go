// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/testport"
)

// Enter through the daemon's real boot. Loading TOML and calling an engine
// setter by hand would miss a disconnected startup wire.
func TestFullBoardPolicyReachesActualDaemonFromTOML(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "-h"))
	dir := privateDaemonFixtureDir(t)
	for name, body := range map[string]string{"local.secret": "capacity-fixture-secret", "dibs.toml": "[limits]\nfull_board = \"refuse\"\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := procexec.Command(ctx, bin, "--allow-parallel", "--dir", dir, "--addr", port.Addr)
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_NOTIFY=off")
	port.Release(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); <-done })
	ready := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		conn, err := net.DialTimeout("tcp", port.Addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("actual daemon never served configured policy")
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+port.Addr+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("X-Dibs-Local", "capacity-fixture-secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var envelope map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		result, ok := envelope["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s response: %v", name, envelope)
		}
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	registered := call("register", map[string]any{"name": "policy-reader", "nonce": "policy-reader-nonce"})
	token, ok := registered["token"].(string)
	if !ok || token == "" {
		t.Fatalf("registration setup: %v", registered)
	}
	listed := call("settings", map[string]any{"token": token})
	for _, item := range listed["settings"].([]any) {
		row := item.(map[string]any)
		if row["setting"] == "limits.full_board" {
			if row["value"] != "refuse" || row["set_by"] != "dibs.toml" {
				t.Fatalf("file default did not reach engine: %v", row)
			}
			return
		}
	}
	t.Fatal("file policy absent from actual daemon settings")
}
