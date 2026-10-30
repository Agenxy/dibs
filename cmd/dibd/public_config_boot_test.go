// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/testport"
)

// No public flags: this is the startup path used by a managed service. The
// operator's real forward is deliberately outside this local loopback proof.
func TestPublicFileBootAndProcessCLI(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"dibd", "dibs"} {
		bin := filepath.Join(binDir, name)
		if out, err := exec.Command("go", "build", "-o", bin, "../"+name).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, out)
		}
		probe := "--help"
		if name == "dibd" {
			probe = "-h"
		}
		testexec.PrepareExecutable(t, exec.Command(bin, probe))
	}
	dir := privateDaemonFixtureDir(t)
	private, public := testport.Reserve(t, "tcp", "127.0.0.1:0"), testport.Reserve(t, "tcp", "127.0.0.1:0")
	var farSideProbes atomic.Int32
	farSide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		farSideProbes.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(farSide.Close)
	config := fmt.Sprintf("addr = %q\n[public]\nforward = %q\naddr = %q\n", private.Addr, farSide.URL, public.Addr)
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	const proof = "public-config-private-proof"
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(proof), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := (invites.Store{Dir: dir}).Mint("forward-broker", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	daemon := exec.CommandContext(ctx, filepath.Join(binDir, "dibd"), "--dir", dir, "--allow-parallel")
	daemon.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_NOTIFY=off")
	daemon.Stdout, daemon.Stderr = log, log
	private.Release(t)
	public.Release(t)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			if err := <-done; err != nil {
				t.Logf("process exited after cancel: %v", err)
			}
		}
		if t.Failed() {
			body, _ := os.ReadFile(logPath)
			t.Log(string(body))
		}
	})
	for _, addr := range []string{private.Addr, public.Addr} {
		ready := false
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			select {
			case err := <-done:
				waited = true
				t.Fatalf("config-only daemon refused startup: %v", err)
			default:
			}
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				ready = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !ready {
			t.Fatal("config-only daemon did not open its listeners")
		}
	}
	cli := func(dataDir, address, invite, token string, args ...string) (map[string]any, string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, filepath.Join(binDir, "dibs"), args...)
		cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_DIR="+dataDir,
			"DIBS_ADDR="+address, "DIBS_INVITE="+invite, "DIBS_TOKEN="+token)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if invite != "" && !strings.HasPrefix(address, "https://") &&
			!strings.Contains(stderr.String(), "plaintext loopback SSH forward") {
			t.Error("legacy invited forward omitted its credential-exposure warning")
		}
		var value map[string]any
		if err == nil && slices.Contains(args, "--json") {
			if decodeErr := json.Unmarshal(stdout.Bytes(), &value); decodeErr != nil {
				t.Fatalf("JSON stdout was contaminated: %v %s", decodeErr, stdout.String())
			}
		} else {
			_ = json.Unmarshal(stdout.Bytes(), &value)
		}
		return value, stdout.String() + stderr.String(), err
	}
	guestDir := privateDaemonFixtureDir(t)
	sender, out, err := cli(guestDir, public.Addr, key, "", "register", "--name", "forward-broker", "--json")
	if err != nil || sender["token"] == nil || sender["agent_id"] != "forward-broker" {
		t.Fatalf("forward CLI register: %v %s", err, out)
	}
	owner, out, err := cli(dir, private.Addr, "", "", "register", "--name", "forward-owner", "--json")
	if err != nil || owner["token"] == nil {
		t.Fatalf("recipient setup: %v %s", err, out)
	}
	sent, out, err := cli(guestDir, public.Addr, key, sender["token"].(string), "send", "--to", "forward-owner", "--json", "job 42: rc=0 state=TERMINAL")
	if err != nil || sent["msg_serial"] == nil {
		t.Fatalf("forward CLI send: %v %s", err, out)
	}
	request := func(address, path, secret string, body []byte) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if path == "/api/transfer-status" {
			req.Method = http.MethodGet
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		if secret != "" {
			req.Header.Set("X-Dibs-Local", secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var value map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&value)
		return resp.StatusCode, value
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "inbox", "arguments": map[string]any{"token": owner["token"]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, inbox := request(private.Addr, "/mcp", proof, body)
	encoded, err := json.Marshal(inbox)
	if err != nil || code != http.StatusOK || !bytes.Contains(encoded, []byte("job 42: rc=0 state=TERMINAL")) ||
		!bytes.Contains(encoded, []byte("forward-broker")) {
		t.Fatalf("recipient inbox did not show completion: %d %v", code, inbox)
	}
	code, status := request(private.Addr, "/api/transfer-status", proof, nil)
	if code != http.StatusOK || status["mode"] != "loopback-forward" || status["public_origin"] != farSide.URL || status["addr"] != public.Addr {
		t.Fatalf("runtime forward status missing or invented: %d %v", code, status)
	}
	for _, path := range []string{"/api/board", "/api/transfer-status", "/files/not-a-ticket"} {
		if code, _ := request(public.Addr, path, "", nil); code != http.StatusForbidden {
			t.Fatalf("forward exposed non-MCP path %s: %d", path, code)
		}
	}
	if code, _ := request(public.Addr, "/mcp", "", []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); code != http.StatusUnauthorized {
		t.Fatal("forward listener accepted a request without an invitation")
	}
	source := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(source, []byte("inline-only forward"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused, out, err := cli(guestDir, public.Addr, key, sender["token"].(string), "put", source, "--json")
	if err == nil || refused["code"] != "E_TRANSFER_UNAVAILABLE" || refused["hint"] == nil {
		t.Fatalf("forward issued an unusable HTTP blob capability: %v %s", err, out)
	}
	_, out, err = cli(dir, private.Addr, "", owner["token"].(string), "invite", "forward-owner-child")
	if err != nil || !strings.Contains(out, "DIBS_ADDR="+farSide.URL) || !strings.Contains(out, "operator's own secure forward") {
		t.Fatalf("actual invitation printed a fictitious HTTPS/native recipe: %v %s", err, out)
	}
	if farSideProbes.Load() != 0 {
		t.Fatal("setup unexpectedly contacted the advertised far-side address")
	}
	doctor, out, _ := cli(dir, private.Addr, "", "", "doctor", "--json")
	if doctor == nil || !strings.Contains(out, "loopback-forward") || !strings.Contains(out, "unmeasured") ||
		strings.Contains(out, "public transfer origin is not an HTTPS origin") {
		t.Fatalf("actual doctor omitted forward state/proof boundary: %s", out)
	}
	if farSideProbes.Load() != 0 {
		t.Fatal("doctor probed the far-side loopback on the wrong machine")
	}
	check := func(extra ...string) (string, error) {
		cmd := exec.CommandContext(ctx, filepath.Join(binDir, "dibd"), append([]string{"--dir", dir, "--check"}, extra...)...)
		cmd.Env = nativeProbeEnv(t.TempDir(), "", "")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// Establish that this same live fixture is a valid replay/check target:
	// otherwise a broken ledger could make every negative probe pass.
	if out, err := check(); err != nil {
		t.Fatalf("valid public file check setup failed: %v %s", err, out)
	}
	for _, bad := range []string{
		`forward="http://192.0.2.1:4778"`,
		`forward="https://127.0.0.1:4778"`,
		`forward="http://user:secret@127.0.0.1:4778"`,
		`forward="http://127.0.0.1:4778/path"`,
		"forward=\"http://127.0.0.1:4778\"\naddr=\"0.0.0.0:4778\"",
		"forward=\"http://127.0.0.1:4778\"\nurl=\"https://board.example.com\"",
		`unknown="must refuse"`,
	} {
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[public]\n"+bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := check()
		if err == nil || (!strings.Contains(out, "[public]") && !strings.Contains(out, "public.unknown")) {
			t.Fatalf("bad public file was not loudly refused: %s: %v %s", bad, err, out)
		}
		boot := exec.CommandContext(ctx, filepath.Join(binDir, "dibd"), "--dir", dir, "--allow-parallel")
		boot.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_NOTIFY=off")
		bootOut, bootErr := boot.CombinedOutput()
		if bootErr == nil || (!bytes.Contains(bootOut, []byte("[public]")) && !bytes.Contains(bootOut, []byte("public.unknown"))) {
			t.Fatalf("ordinary boot did not refuse public configuration before serving: %s: %v %s", bad, bootErr, bootOut)
		}
	}
	// Enter run(), not only the merge helper, to prove real flags reach the
	// file composition. No listener is bound by --check.
	bad := "[public]\nforward=\"http://127.0.0.1:4778\"\naddr=\"0.0.0.0:4778\"\nack_unverified_guest_client=true\nacme_accept_terms=true\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = check("--public-addr", public.Addr, "--ack-unverified-guest-client=false", "--acme-accept-terms=false")
	if err != nil {
		t.Fatalf("real flags did not repair their file values before validation: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}
