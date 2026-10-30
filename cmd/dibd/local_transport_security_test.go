// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/testport"
)

const qFixtureSecret = "q-private-fixture-secret-never-a-production-value"

func qShortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dibs-q-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func qBuildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(qShortDir(t), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	// Help performs no board access, even in a build that reports board status
	// as part of --version. Warm executable verification without a live profile.
	testexec.PrepareExecutable(t, exec.Command(bin, "--help"))
	return bin
}

func qClient(t *testing.T, bin, dir, addr, stdin string, args ...string) ([]byte, error) {
	return qClientEnv(t, bin, dir, addr, stdin, nil, args...)
}

func qClientEnv(t *testing.T, bin, dir, addr, stdin string, extra []string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_DIR="+dir, "DIBS_ADDR="+addr, "DIBS_TRANSPORT=local")
	cmd.Env = append(cmd.Env, extra...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		t.Fatalf("client deadline: %v %s", ctx.Err(), stderr.String())
	}
	return out, err
}

func TestLocalIPCProfilesRetainMCPFilesAndConnectedClientEvidence(t *testing.T) {
	bin := qBuildCLI(t)
	dir := qShortDir(t)
	addr, _ := qStartBoard(t, dir)
	for _, call := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"q-fixture","version":"1"},"capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
	} {
		out, err := qClient(t, bin, dir, addr, call+"\n", "mcp-stdio")
		if err != nil || qRPCReply(t, out, 1)["error"] != nil {
			t.Fatalf("MCP positive control: %v %s", err, out)
		}
	}
	out, err := qClient(t, bin, dir, addr, "", "register", "--name", "q-file-owner", "--json")
	var agent map[string]any
	if err != nil || json.Unmarshal(out, &agent) != nil {
		t.Fatalf("register control: %v %s", err, out)
	}
	token, ok := agent["token"].(string)
	if !ok || token == "" {
		t.Fatal("setup: register returned no token")
	}
	source := filepath.Join(dir, "source.bin")
	want := []byte("IPC byte plane: ticket and body stay off the TCP listener.\n")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = qClientEnv(t, bin, dir, addr, "", []string{"DIBS_TOKEN=" + token}, "put", source, "--json")
	var stored map[string]any
	if err != nil || json.Unmarshal(out, &stored) != nil {
		t.Fatalf("IPC put: %v %s", err, out)
	}
	blob, ok := stored["blob"].(string)
	if !ok || !strings.HasPrefix(blob, "sha256:") {
		t.Fatal("put did not return a committed blob")
	}
	dest := filepath.Join(dir, "download.bin")
	if out, err := qClientEnv(t, bin, dir, addr, "", []string{"DIBS_TOKEN=" + token}, "get", blob, "-o", dest, "--json"); err != nil {
		t.Fatalf("IPC get: %v %s", err, out)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("byte plane did not preserve bytes: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bridge := exec.CommandContext(ctx, bin, "mcp-stdio")
	bridge.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_DIR="+dir, "DIBS_ADDR="+addr, "DIBS_TRANSPORT=local")
	in, err := bridge.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := bridge.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = bridge.Wait() }()
	if _, err := io.WriteString(in, `{"jsonrpc":"2.0","id":9,"method":"server/discover","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(pipe).ReadBytes('\n')
	if err != nil || qRPCReply(t, line, 9)["error"] != nil {
		t.Fatalf("live bridge setup: %v %s", err, line)
	}
	out, _ = qClient(t, bin, dir, addr, "", "doctor", "--json")
	if !bytes.Contains(out, []byte("reported bridge pid")) || !bytes.Contains(out, []byte("uses local-ipc")) {
		t.Fatalf("doctor did not report the actual still-connected bridge's IPC transport: %s", out)
	}
}

// One actual daemon process, rather than a handler assembled with a setter
// that production might never call. This fixture is also compiled on old code.
func TestQDaemonChild(t *testing.T) {
	if os.Getenv("DIBS_Q_DAEMON_CHILD") != "1" {
		return
	}
	os.Args = []string{"dibd", "-dir", os.Getenv("DIBS_DIR"), "-addr", os.Getenv("DIBS_ADDR"), "-allow-parallel"}
	flag.CommandLine = cloudFlagSet()
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func qStartBoard(t *testing.T, dir string) (string, func()) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(qFixtureSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake]\nsockets = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	cmd := exec.Command(os.Args[0], "-test.run=^TestQDaemonChild$")
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_Q_DAEMON_CHILD=1", "DIBS_DIR="+dir, "DIBS_ADDR="+port.Addr)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	port.Release(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("private daemon shutdown: %v %s", err, logs.String())
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				_ = <-done
				t.Error("private daemon did not stop")
			}
		})
	}
	t.Cleanup(stop)
	ready := false
	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-done:
			done <- err // Cleanup must still be able to reap the failed setup.
			t.Fatalf("setup: daemon exited before readiness: %v %s", err, logs.String())
		default:
		}
		resp, err := client.Get("http://" + port.Addr + "/livez")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("setup: actual daemon never became ready")
	}
	return port.Addr, stop
}

func qRPCReply(t *testing.T, out []byte, id float64) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(out, []byte("\n")) {
		var value map[string]any
		if json.Unmarshal(line, &value) == nil && value["id"] == id {
			return value
		}
	}
	t.Fatalf("no matching RPC reply for %.0f: %s", id, out)
	return nil
}

func TestLocalClientsNeverDiscloseToReboundTCP(t *testing.T) {
	bin := qBuildCLI(t)
	dir := qShortDir(t)
	addr, stop := qStartBoard(t, dir)
	if out, err := qClient(t, bin, dir, addr, "", "board", "--json"); err != nil || !json.Valid(bytes.TrimSpace(out)) {
		t.Fatalf("setup: legitimate CLI failed: %v %s", err, out)
	}
	rpc := `{"jsonrpc":"2.0","id":7,"method":"server/discover","params":{}}` + "\n"
	if out, err := qClient(t, bin, dir, addr, rpc, "mcp-stdio"); err != nil || qRPCReply(t, out, 7)["error"] != nil {
		t.Fatalf("setup: legitimate stdio request failed: %v %s", err, out)
	}
	stop()
	var requests, secrets atomic.Int32
	squatter := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Dibs-Local") == qFixtureSecret {
			secrets.Add(1)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/mcp" {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":7,"result":{}}`)
			return
		}
		_, _ = io.WriteString(w, `{"agents":[],"claims":[],"serial":0}`)
	}))
	_ = squatter.Listener.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("setup: could not occupy the stopped daemon's exact port: %v", err)
	}
	squatter.Listener = ln
	squatter.Start()
	defer squatter.Close()
	_, _ = qClient(t, bin, dir, addr, "", "board", "--json")
	// A normal tool call uses the existing ten-second restart grace, rather
	// than startup discovery's twenty-five seconds. The invariant is unchanged:
	// the substituted TCP listener must see no request, body or secret.
	probeRPC := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"check_in","arguments":{"token":"fixture-unknown"}}}` + "\n"
	out, _ := qClient(t, bin, dir, addr, probeRPC, "mcp-stdio")
	qRPCReply(t, out, 7)
	if requests.Load() != 0 || secrets.Load() != 0 {
		t.Fatalf("squatted TCP received %d requests and %d local secrets from real CLI/bridge; no local credential may leave before server identity is verified", requests.Load(), secrets.Load())
	}
}
