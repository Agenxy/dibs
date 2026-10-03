package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The SHIPPED command, guestTLS and publicGate, not a transport constructed by
// the test. Before --guest exists both paths must fail, not silently skip or
// count an unknown-argument exit as a successful TLS refusal.
func TestGuestBridgeRealCommand(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	build := exec.Command("go", "build", "-o", bin, "../dibs")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture command: %v %s", err, out)
	}
	ip := netip.MustParseAddr("::1")
	caDir := t.TempDir()
	config, caPEM, pin, err := guestTLS(caDir, ip)
	if err != nil {
		t.Fatal(err)
	}
	ca, signer, err := ensureGuestCA(caDir, ip)
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(ips []net.IP, dns []string) tls.Certificate {
		cert := signGuestFixture(t, ca, signer, &x509.Certificate{IPAddresses: ips, DNSNames: dns}, &signer.PublicKey)
		return tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: signer}
	}
	intKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	intermediate := signGuestFixture(t, ca, signer, &x509.Certificate{
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}, &intKey.PublicKey)
	intLeaf := signGuestFixture(t, intermediate, intKey, &x509.Certificate{
		IPAddresses: []net.IP{net.IPv6loopback},
	}, &signer.PublicKey)
	expired := *config.Certificates[0].Leaf
	expired.NotBefore, expired.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	expiredDER, err := x509.CreateCertificate(rand.Reader, &expired, ca, expired.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	other, otherPEM, _, err := guestTLS(t.TempDir(), ip)
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := filepath.Join(t.TempDir(), "other-trusted-root.pem")
	if err := os.WriteFile(controlRoot, []byte(otherPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	// This same-IP control is valid under a broader verifier. The process's
	// system-root override below still must not expand the guest's root pool.
	nativeFixturePreflight(t, controlRoot, "::1", other.Certificates[0], true)
	for _, tc := range []struct {
		name   string
		cert   tls.Certificate
		accept bool
	}{
		{"valid guest", config.Certificates[0], true},
		{"renewed leaf same CA", leaf([]net.IP{net.IPv6loopback}, nil), true},
		{"different CA same IP", other.Certificates[0], false},
		{"wrong IP", leaf([]net.IP{net.ParseIP("::2")}, nil), false},
		{"mixed IPv6", leaf([]net.IP{net.IPv6loopback, net.ParseIP("::2")}, nil), false},
		{"mixed IPv4", leaf([]net.IP{net.IPv6loopback, net.IPv4(127, 0, 0, 1)}, nil), false},
		{"DNS leaf", leaf(nil, []string{"localhost"}), false},
		{"mixed DNS", leaf([]net.IP{net.IPv6loopback}, []string{"unrelated.example"}), false},
		{"expired leaf", tls.Certificate{Certificate: [][]byte{expiredDER}, PrivateKey: signer}, false},
		{"intermediate bypass", tls.Certificate{
			Certificate: [][]byte{intLeaf.Raw, intermediate.Raw}, PrivateKey: signer,
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, key, hits := nativeGuestFixture(t, tc.cert, "::1")
			for _, legacy := range []bool{false, true} {
				request := map[string]any{
					"jsonrpc": "2.0", "id": 17, "method": "server/discover",
					"params": map[string]any{"_meta": map[string]any{
						"io.modelcontextprotocol/protocolVersion": "2026-07-28",
					}},
				}
				if legacy {
					request["method"] = "initialize"
					request["params"] = map[string]any{
						"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
						"clientInfo": map[string]any{"name": "guest-command-fixture", "version": "1"},
					}
				}
				file := guestCommandRecipe(t, endpoint, key, caPEM, pin)
				line, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bin, "mcp-stdio", "--guest", file)
				cmd.Dir = t.TempDir()
				// Poison local board state: guest mode must neither need a daemon
				// here nor send a caller's local secret/proxy to the guest endpoint.
				cmd.Env = append(nativeProbeEnv(cmd.Dir, "", ""),
					"DIBS_DIR="+filepath.Join(cmd.Dir, "no-local-board"),
					"DIBS_ADDR=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "SSL_CERT_FILE="+controlRoot)
				cmd.Stdin = bytes.NewReader(append(line, '\n'))
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("shipped guest command exited (legacy=%v): %v; stderr=%s", legacy, err, stderr.String())
				}
				var reply struct {
					ID     int            `json:"id"`
					Result map[string]any `json:"result"`
					Error  any            `json:"error"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
					t.Fatalf("stdout is not the caller's JSON-RPC reply: %v %s", err, stdout.Bytes())
				}
				if reply.ID != 17 || (reply.Error == nil) != tc.accept {
					t.Fatalf("actual discovery failed (legacy=%v): %s", legacy, stdout.Bytes())
				}
				if tc.accept && reply.Result == nil {
					t.Fatal("fake discovery success without server result")
				}
				if tc.accept && !legacy && reply.Result["resultType"] != "complete" {
					t.Fatalf("modern result downgraded: %s", stdout.Bytes())
				}
				if tc.accept {
					result := reply.Result
					if value, ok := result["value"].(map[string]any); ok {
						result = value
					}
					caps, ok := result["capabilities"].(map[string]any)
					if !ok {
						t.Fatalf("discovery lacks capabilities: %s", stdout.Bytes())
					}
					for _, name := range []string{"tools", "resources"} {
						capability, _ := caps[name].(map[string]any)
						if capability["subscribe"] == true || capability["listChanged"] == true {
							t.Fatalf("pull-only invitation advertised push: %s", stdout.Bytes())
						}
					}
					if !legacy && result["cacheScope"] != "private" {
						t.Fatal("authorization-specific discovery allowed a shared cache")
					}
				}
			}
			want := int64(0)
			if tc.accept {
				want = 2
			}
			if hits.Load() != want {
				t.Fatalf("real invitation gate got %d requests, want %d", hits.Load(), want)
			}
		})
	}
}

func TestGuestBridgeMailboxRecovery(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build actual bridge: %v %s", err, out)
	}
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"issuer nonce", "caller nonce", "legacy private store"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, key, hits := nativeGuestFixture(t, config.Certificates[0], "::1")
			file := guestCommandRecipe(t, endpoint, key, pem, pin)
			if mode == "legacy private store" {
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var recipe map[string]any
				if err := json.Unmarshal(body, &recipe); err != nil {
					t.Fatal(err)
				}
				delete(recipe, "recovery_nonce")
				body, err = json.Marshal(recipe)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := map[string]any{"name": "native-guest", "kind": "persistent", "description": "bridge test"}
			if mode == "caller nonce" {
				args["nonce"] = "caller-retained-nonce-abcdef0123456789abcdef0123456789"
			}
			request, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 19, "method": "tools/call",
				"params": map[string]any{"name": "register", "arguments": args, "_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var firstID, firstToken string
			for run := range 2 {
				out := guestCommandOutput(t, bin, file, request)
				var reply struct {
					Result struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
						IsError bool `json:"isError"`
					} `json:"result"`
				}
				if err := json.Unmarshal(out, &reply); err != nil {
					t.Fatalf("real register reply: %v %s", err, out)
				}
				if reply.Result.IsError || len(reply.Result.Content) == 0 {
					t.Fatalf("registration refused in fresh process %d: %s", run, out)
				}
				var registration struct {
					ID    string `json:"agent_id"`
					Token string `json:"token"`
				}
				if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &registration); err != nil {
					t.Fatal(err)
				}
				if registration.ID == "" || registration.Token == "" {
					t.Fatalf("register did not issue identity: %s", out)
				}
				if run == 0 {
					firstID, firstToken = registration.ID, registration.Token
				} else if registration.ID != firstID || registration.Token != firstToken {
					t.Fatal("fresh bridge created a sibling rather than recovering the bound mailbox")
				}
			}
			if hits.Load() != 2 {
				t.Fatalf("register retried/replayed: %d requests", hits.Load())
			}
		})
	}
}

func guestCommandOutput(t *testing.T, bin, file string, request []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "mcp-stdio", "--guest", file)
	cmd.Dir = t.TempDir() // a fresh container-equivalent HOME, not the recipe's directory
	for _, v := range nativeProbeEnv(cmd.Dir, "", "") {
		if !strings.HasPrefix(v, "NO_PROXY=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "DIBS_DIR="+filepath.Join(cmd.Dir, "no-local-board"),
		"DIBS_ADDR=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1")
	cmd.Stdin = bytes.NewReader(append(bytes.Clone(request), '\n'))
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("guest bridge exited: %v %s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "no-local-board")); !os.IsNotExist(err) {
		t.Fatal("guest bridge initialized local board state")
	}
	return out.Bytes()
}

func TestGuestBridgeInvalidRecipeBeforeHTTP(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build actual bridge: %v %s", err, out)
	}
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, key, hits := nativeGuestFixture(t, config.Certificates[0], "::1")
	for _, mode := range []string{
		"public file", "public directory", "symlink", "relative path", "duplicate", "case alias", "unknown field",
		"expired", "bad key", "wrong pin", "extra PEM", "DNS URL", "IPv4 URL", "query", "fragment", "userinfo",
	} {
		t.Run(mode, func(t *testing.T) {
			file := guestCommandRecipe(t, endpoint, key, pem, pin)
			badGuestRecipe(t, file, mode)
			if mode == "relative path" {
				file = filepath.Base(file)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "mcp-stdio", "--guest", file)
			cmd.Dir, cmd.Env = t.TempDir(), nativeProbeEnv(t.TempDir(), "", "")
			cmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"server/discover\"}\n")
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			if err := cmd.Run(); err == nil || out.Len() != 0 || stderr.Len() == 0 {
				t.Fatal("invalid private permission recipe was accepted or failed silently")
			}
			if strings.Contains(stderr.String(), key) {
				t.Fatal("invalid recipe diagnostics disclosed its credential")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("invalid recipe was allowed to send an invitation credential over HTTP")
	}
}

func badGuestRecipe(t *testing.T, file, mode string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var recipe map[string]any
	if err := json.Unmarshal(b, &recipe); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "public file":
		err = os.Chmod(file, 0o644)
	case "public directory":
		err = os.Chmod(filepath.Dir(file), 0o755)
	case "symlink":
		original := file + ".original"
		if err = os.Rename(file, original); err == nil {
			err = os.Symlink(original, file)
		}
	case "relative path":
		return
	case "duplicate":
		b = append([]byte(`{"endpoint":"https://[::1]:1/mcp",`), b[1:]...)
	case "unknown field":
		recipe["unknown"] = true
	case "case alias":
		recipe["Endpoint"] = recipe["endpoint"]
	case "expired":
		recipe["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	case "bad key":
		recipe["invitation_key"] = "not-an-invitation-credential-0123456789"
	case "wrong pin":
		recipe["ca_spki_sha256"] = strings.Repeat("0", 64)
	case "extra PEM":
		recipe["ca_pem"] = recipe["ca_pem"].(string) + recipe["ca_pem"].(string)
	case "DNS URL":
		recipe["endpoint"] = "https://localhost:443/mcp"
	case "IPv4 URL":
		recipe["endpoint"] = "https://127.0.0.1:443/mcp"
	case "query", "fragment", "userinfo":
		url := recipe["endpoint"].(string)
		switch mode {
		case "query":
			url += "?x=1"
		case "fragment":
			url += "#x"
		case "userinfo":
			url = strings.Replace(url, "https://", "https://user@", 1)
		}
		recipe["endpoint"] = url
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "public file" || mode == "public directory" || mode == "symlink" {
		return
	}
	if mode != "duplicate" {
		b, err = json.Marshal(recipe)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func guestCommandRecipe(t *testing.T, endpoint, key, caPEM, pin string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{
		"schema_version": 1, "name": "native-guest", "endpoint": endpoint,
		"invitation_key": key, "ca_pem": caPEM, "ca_spki_sha256": pin,
		"recovery_nonce": "guest-fixture-retained-nonce-0123456789abcdef0123456789abcdef",
		"expires_at":     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "invitation.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}
