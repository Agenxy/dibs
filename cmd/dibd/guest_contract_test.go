package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/selfupdate"
)

// This is a compiled-floor fixture, not a production config escape hatch.
// The same TLS/publicGate/MCP door as the shipped guest command is exercised;
// no handshake state or caller clientInfo may supply bridge-version evidence.
func TestGuestContractFloorEveryInvitedRequest(t *testing.T) {
	prior := selfupdate.GuestSupportingMinimum
	selfupdate.GuestSupportingMinimum = "v0.0.9"
	t.Cleanup(func() { selfupdate.GuestSupportingMinimum = prior })
	config, pem, _, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, key, _ := nativeGuestFixture(t, config.Certificates[0], "::1")
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		t.Fatal("guest CA setup failed")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
	defer client.CloseIdleConnections()
	var additionalVersions []string
	post := func(method, version, session string, notification bool) (map[string]any, string) {
		t.Helper()
		params := map[string]any{"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo":      map[string]any{"name": "untrusted-caller", "version": "99.0.0"},
		}}
		if method == "initialize" {
			params = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "99.0.0"}}
		}
		if method == "tools/call" {
			params["name"] = "register"
			params["arguments"] = map[string]any{"name": "native-guest", "nonce": "guest-contract-retained-fixture-nonce", "kind": "persistent"}
		}
		wire := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if !notification {
			wire["id"] = 23
		}
		body, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		if version != "" {
			req.Header.Set("X-Dibs-Guest-Version", version)
		}
		for _, extra := range additionalVersions {
			req.Header.Add("X-Dibs-Guest-Version", extra)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if notification {
			if resp.StatusCode != http.StatusAccepted || len(b) != 0 {
				t.Fatalf("notification got synthesized reply: status%d %s", resp.StatusCode, b)
			}
			return nil, ""
		}
		var out map[string]any
		if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out["id"] != float64(23) {
			t.Fatalf("fixture RPC setup failed: status%d %s", resp.StatusCode, b)
		}
		return out, resp.Header.Get("Mcp-Session-Id")
	}
	for _, method := range []string{"server/discover", "tools/list", "tools/call", "initialize", "resources/list", "tasks/get", "ping"} {
		// Independent invitations keep the intentionally bounded public rate
		// limiter out of this compatibility discriminator; never disable it.
		endpoint, key, _ = nativeGuestFixture(t, config.Certificates[0], "::1")
		for _, version := range []string{"", "0.0.8", "devel", "0.0.9-rc.1", "0.0.10-0.20261003000000-abcdef012345", "v0.0.9+dirty", "not-a-version"} {
			out, _ := post(method, version, "", false)
			failure, _ := out["error"].(map[string]any)
			data, _ := failure["data"].(map[string]any)
			if data["code"] != "E_GUEST_VERSION" || !strings.Contains(data["hint"].(string), "v0.0.9") {
				t.Fatalf("%s version%q bypassed compatibility floor: %v", method, version, out)
			}
		}
	}
	// The unpublished checkpoint does not claim support or change ordinary
	// invitation behavior. Its supporting floor remains unset in production.
	selfupdate.GuestSupportingMinimum = ""
	unset, _ := post("server/discover", "", "", false)
	if unset["error"] != nil || unset["result"] == nil {
		t.Fatal("unset floor broke existing invitation discovery")
	}
	selfupdate.GuestSupportingMinimum = "v0.0.9"
	for _, version := range []string{"0.0.9", "v0.0.10"} {
		for _, method := range []string{"server/discover", "tools/list", "tools/call", "initialize"} {
			out, _ := post(method, version, "", false)
			result, _ := out["result"].(map[string]any)
			if out["error"] != nil || result == nil || result["isError"] == true {
				t.Fatalf("supporting bridge refused at %s: %v", method, out)
			}
		}
	}
	_, session := post("initialize", "0.0.9", "", false)
	if session == "" {
		t.Fatal("legacy session setup did not run")
	}
	out, _ := post("tools/list", "", session, false)
	if out["error"] == nil {
		t.Fatal("legacy session bypassed per-request version floor")
	}
	post("notifications/initialized", "0.0.8", session, true)
	post("tools/call", "0.0.8", session, true)
	endpoint, key, _ = nativeGuestFixture(t, config.Certificates[0], "::1")
	additionalVersions = []string{"0.0.10"}
	duplicate, _ := post("server/discover", "0.0.9", "", false)
	if duplicate["error"] == nil {
		t.Fatal("ambiguous duplicate version headers admitted")
	}
	additionalVersions = nil
	selfupdate.GuestSupportingMinimum = "invalid"
	broken, _ := post("server/discover", "0.0.9", "", false)
	failure, _ := broken["error"].(map[string]any)
	data, _ := failure["data"].(map[string]any)
	if data["code"] != "E_GUEST_CONTRACT" || !strings.Contains(data["hint"].(string), "repair this board build") {
		t.Fatal("invalid compiled floor gave a bogus guest upgrade hint")
	}
}

func TestGuestContractVersionComesFromShippedBridge(t *testing.T) {
	prior := selfupdate.GuestSupportingMinimum
	selfupdate.GuestSupportingMinimum = "v0.0.9"
	t.Cleanup(func() { selfupdate.GuestSupportingMinimum = prior })
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, key, _ := nativeGuestFixture(t, config.Certificates[0], "::1")
	file := guestCommandRecipe(t, endpoint, key, pem, pin)
	for _, version := range []string{"0.0.8", "0.0.9", "devel"} {
		bin := filepath.Join(t.TempDir(), "dibs")
		cmd := exec.Command("go", "build", "-ldflags", "-X github.com/agenxy/dibs/internal/build.Version="+version, "-o", bin, "../dibs")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build actual versioned bridge: %v %s", err, out)
		}
		for _, line := range []string{
			`{"jsonrpc":"2.0","id":29,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"forged-caller","version":"99.0.0"}}}}`,
			`{"jsonrpc":"2.0","id":29,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			`{"jsonrpc":"2.0","id":29,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"forged-caller","version":"99.0.0"}}}`,
		} {
			b := guestCommandOutput(t, bin, file, []byte(line))
			var out map[string]any
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			failure, _ := out["error"].(map[string]any)
			data, _ := failure["data"].(map[string]any)
			if version == "0.0.9" {
				if out["result"] == nil || failure != nil {
					t.Fatalf("shipped supporting bridge refused: %s", b)
				}
			} else if data["code"] != "E_GUEST_VERSION" {
				t.Fatalf("actual bridge%q bypassed floor: %s", version, b)
			}
		}
	}
}
