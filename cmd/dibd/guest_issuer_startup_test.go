package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/invites"
)

// Start the shipped daemon, not a Service whose recovery callback the test
// installs by hand. A TLS-proxy origin is operator configuration here; this
// probe tests issuer startup/Bind/replay, not TLS or external reachability.
func TestGuestIssuerRecoveryThroughActualDaemonStartup(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v %s", err, out)
	}
	dir := t.TempDir()
	hash, err := adminpw.Hash("fixture-operator")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"admin.hash": hash, "local.secret": "fixture-local-secret"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	private, public := guestIssuerFreeAddress(t), guestIssuerFreeAddress(t)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	post := func(base, path string, payload any, bearer string) map[string]any {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if base == private {
			req.Header.Set("X-Dibs-Local", "fixture-local-secret")
			req.Header.Set("X-Dibs-Admin", "fixture-operator")
		} else {
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 {
			t.Fatalf("fixture request %s: status %d, decode %v", path, resp.StatusCode, err)
		}
		return out
	}
	start := func(origin string) func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, bin, "--allow-parallel", "--dir", dir, "--addr", private,
			"--public-url", origin, "--public-addr", public)
		cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_DIR="+dir)
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if !stopped {
				stopped = true
				cancel()
				<-done
			}
		}
		t.Cleanup(stop)
		for _, addr := range []string{private, public} {
			ready := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					ready = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !ready {
				t.Fatalf("actual daemon never served fixture %s", addr)
			}
		}
		return stop
	}
	mint := func() map[string]any {
		t.Helper()
		out := post(private, "/api/admin/invites", map[string]any{"name": "guest-worker", "ttl_s": 3600}, "")
		nonce, _ := out["recovery_nonce"].(string)
		raw, err := hex.DecodeString(nonce)
		if err != nil || len(raw) != 32 {
			t.Fatal("actual issuer startup omitted its 256-bit recovery nonce")
		}
		expires, _ := out["expires_at"].(string)
		parsed, err := time.Parse(time.RFC3339Nano, expires)
		entries, serr := (invites.Store{Dir: dir}).List()
		if err != nil || serr != nil || len(entries) != 1 || !entries[0].Expires.Equal(parsed) {
			t.Fatal("issuer expiry differs from the stored credential's actual expiry")
		}
		return out
	}
	bind := func(out map[string]any) string {
		t.Helper()
		key, _ := out["key"].(string)
		rpc := post(public, "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "register", "arguments": map[string]any{
				"name": "guest-worker", "nonce": out["recovery_nonce"], "kind": "persistent",
			}}}, key)
		result, _ := rpc["result"].(map[string]any)
		contents, _ := result["content"].([]any)
		if rpc["error"] != nil || result["isError"] == true || len(contents) == 0 {
			t.Fatal("actual public gate refused issuer nonce recovery")
		}
		block, _ := contents[0].(map[string]any)
		text, _ := block["text"].(string)
		var registered map[string]any
		if err := json.Unmarshal([]byte(text), &registered); err != nil {
			t.Fatal(err)
		}
		id, _ := registered["agent_id"].(string)
		if id == "" {
			t.Fatal("register setup returned no agent id")
		}
		return id
	}
	stop := start("https://[::1]:41001")
	first := mint()
	id := bind(first)
	post(private, "/api/admin/invites", map[string]any{"action": "revoke", "name": "guest-worker"}, "")
	stop()
	client.CloseIdleConnections()
	start("https://[::1]:41002")
	second := mint()
	if first["key"] == second["key"] || first["url"] == second["url"] {
		t.Fatal("reissue setup did not change credential and endpoint")
	}
	if first["recovery_nonce"] != second["recovery_nonce"] || bind(second) != id {
		t.Fatal("restart/reissue stranded the existing guest mailbox")
	}
	listed := post(private, "/api/admin/invites", map[string]any{"action": "list"}, "")
	listing, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, generation := range []map[string]any{first, second} {
		for _, field := range []string{"key", "recovery_nonce"} {
			secret, _ := generation[field].(string)
			if secret == "" || bytes.Contains(listing, []byte(secret)) {
				t.Fatal("invitation list exposed an issuance credential")
			}
			for _, name := range []string{"invites.json", "ledger.jsonl"} {
				stored, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || bytes.Contains(stored, []byte(secret)) {
					t.Fatal("issuance credential leaked into plaintext access metadata or ledger")
				}
			}
		}
	}
}

func guestIssuerFreeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
