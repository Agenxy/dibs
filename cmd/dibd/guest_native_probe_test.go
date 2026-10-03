package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
)

// Opt-in measurement of INSTALLED clients, not source-tree verifier substitutes.
// Each subprocess has its own config directory and makes only MCP inventory
// calls. Never create/resume a thread, send a model prompt, edit user roots or
// disable certificate verification. Loopback is a fixture, not WAN acceptance.
func TestNativeGuestTrustProbe(t *testing.T) {
	clients := map[string]string{
		"codex":  os.Getenv("DIBS_NATIVE_CODEX"),
		"claude": os.Getenv("DIBS_NATIVE_CLAUDE"),
	}
	if clients["codex"] == "" && clients["claude"] == "" {
		t.Skip("set DIBS_NATIVE_CODEX / DIBS_NATIVE_CLAUDE to exact installed binaries")
	}
	for name, binary := range clients {
		if binary == "" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			nativeVersion(t, binary)
			nativeGuestMatrix(t, name, binary)
		})
	}
}

func nativeVersion(t *testing.T, binary string) {
	t.Helper()
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("binary digest: %v / %v", copyErr, closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Dir = t.TempDir()
	cmd.Env = nativeProbeEnv(cmd.Dir, "", "")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed version: %v %s", err, out)
	}
	t.Logf("installed %s sha256=%x version=%s", binary, hash.Sum(nil), bytes.TrimSpace(out))
}

func nativeGuestMatrix(t *testing.T, client, binary string) {
	t.Helper()
	dir := t.TempDir()
	ip := netip.MustParseAddr("::1")
	tlsConfig, caPEM, _, err := guestTLS(dir, ip)
	if err != nil {
		t.Fatal(err)
	}
	ca, signer, err := ensureGuestCA(dir, ip)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "native-root.pem")
	if err := os.WriteFile(caPath, []byte(caPEM), 0o600); err != nil {
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
	// A permissive fixture root is a control ONLY. Production never offers it.
	controlRoot := *ca
	controlRoot.PermittedIPRanges = nil
	controlRoot.ExcludedDNSDomains = nil
	controlRoot.PermittedDNSDomainsCritical = false
	controlDER, err := x509.CreateCertificate(rand.Reader, &controlRoot, &controlRoot, &signer.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(dir, "control-root.pem")
	if err := os.WriteFile(controlPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: controlDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	// Isolate the two name-constraint encodings. A rejection of the combined
	// production root is NOT evidence that empty dNSName alone caused it.
	emptyOnly := *ca
	emptyOnly.PermittedIPRanges = nil
	emptyOnlyPath := nativeControlRoot(t, dir, "empty-only", &emptyOnly, signer)
	ipOnly := *ca
	ipOnly.ExcludedDNSDomains = nil
	ipOnlyPath := nativeControlRoot(t, dir, "ip-only", &ipOnly, signer)
	expiredTemplate := *tlsConfig.Certificates[0].Leaf
	expiredTemplate.NotBefore = time.Now().Add(-2 * time.Hour)
	expiredTemplate.NotAfter = time.Now().Add(-time.Hour)
	expiredDER, err := x509.CreateCertificate(rand.Reader, &expiredTemplate, ca,
		tlsConfig.Certificates[0].Leaf.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	mixedIntLeaf := signGuestFixture(t, intermediate, intKey, &x509.Certificate{
		IPAddresses: []net.IP{net.IPv6loopback, net.ParseIP("::2")},
		DNSNames:    []string{"unrelated.example"},
	}, &signer.PublicKey)
	intCert := func(ips []net.IP, dns []string) tls.Certificate {
		cert := signGuestFixture(t, intermediate, intKey,
			&x509.Certificate{IPAddresses: ips, DNSNames: dns}, &signer.PublicKey)
		return tls.Certificate{Certificate: [][]byte{cert.Raw, intermediate.Raw}, PrivateKey: signer}
	}
	tests := []struct {
		name, root, host string
		cert             tls.Certificate
		accept           bool
	}{
		{"permissive-control", controlPath, "::1", tlsConfig.Certificates[0], true},
		{"permissive-dns-control", controlPath, "localhost", leaf(nil, []string{"localhost"}), true},
		{"permissive-ipv4-control", controlPath, "127.0.0.1", leaf([]net.IP{net.IPv4(127, 0, 0, 1)}, nil), true},
		{"empty-dns-only-ipv4-control", emptyOnlyPath, "127.0.0.1", leaf([]net.IP{net.IPv4(127, 0, 0, 1)}, nil), true},
		{"ipv6-constraint-only-dns-control", ipOnlyPath, "localhost", leaf(nil, []string{"localhost"}), true},
		{"ipv6-constraint-only-ip-control", ipOnlyPath, "::1", tlsConfig.Certificates[0], true},
		{"production-empty-dns", caPath, "::1", tlsConfig.Certificates[0], true},
		{"unknown-root", "", "::1", tlsConfig.Certificates[0], false},
		{"expired-leaf", caPath, "::1", tls.Certificate{
			Certificate: [][]byte{expiredDER}, PrivateKey: tlsConfig.Certificates[0].PrivateKey,
		}, false},
		{"other-ip", caPath, "::1", leaf([]net.IP{net.ParseIP("::2")}, nil), false},
		{"mixed-ip-v6", caPath, "::1", leaf([]net.IP{net.IPv6loopback, net.ParseIP("::2")}, nil), false},
		{"mixed-ip-v4", caPath, "::1", leaf([]net.IP{net.IPv6loopback, net.ParseIP("192.0.2.1")}, nil), false},
		{"mixed-ip-dns", caPath, "::1", leaf([]net.IP{net.IPv6loopback}, []string{"unrelated.example"}), false},
		{"mixed-reserved-dns", caPath, "::1", leaf([]net.IP{net.IPv6loopback}, []string{"invalid", "anything.invalid"}), false},
		{"dns-only", caPath, "localhost", leaf(nil, []string{"localhost"}), false},
		{"intermediate-bypass", caPath, "::1", tls.Certificate{
			Certificate: [][]byte{intLeaf.Raw, intermediate.Raw}, PrivateKey: signer,
		}, false},
		{"intermediate-out-of-scope", caPath, "::1", tls.Certificate{
			Certificate: [][]byte{mixedIntLeaf.Raw, intermediate.Raw}, PrivateKey: signer,
		}, false},
		{"intermediate-dns-only", caPath, "localhost", intCert(nil, []string{"localhost"}), false},
		{"intermediate-out-v6", caPath, "::1", intCert([]net.IP{net.IPv6loopback, net.ParseIP("::2")}, nil), false},
		{"intermediate-mixed-dns", caPath, "::1", intCert([]net.IP{net.IPv6loopback}, []string{"unrelated.example"}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Assert the selected certificate fixture against Go before trusting
			// native results. This is setup proof, not native acceptance.
			if tc.root != "" {
				nativeFixturePreflight(t, tc.root, tc.host, tc.cert, tc.accept)
			}
			url, bearer, requests := nativeGuestFixture(t, tc.cert, tc.host)
			connected, detail, err := nativeInventory(t, client, binary, tc.root, url, bearer)
			if err != nil {
				t.Fatalf("probe setup/protocol failed: %v; %s", err, detail)
			}
			t.Logf("connected=%v HTTP-requests=%d native=%s", connected, requests.Load(), detail)
			if connected != tc.accept {
				t.Errorf("native %s %s connected=%v want=%v", client, tc.name, connected, tc.accept)
			}
			if tc.accept && requests.Load() == 0 {
				t.Error("expected positive fixture not reached")
			}
			if !tc.accept && requests.Load() != 0 {
				t.Error("unacceptable certificate let HTTP credentials reach the server")
			}
		})
	}
}

func nativeFixturePreflight(t *testing.T, root, host string, cert tls.Certificate, accept bool) {
	t.Helper()
	rootPEM, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		t.Fatal("control root is not parseable PEM")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	intermediates := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		parsed, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		intermediates.AddCert(parsed)
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, DNSName: host})
	if (err == nil) != accept {
		t.Fatalf("fixture disagrees with Go preflight: accept=%v error=%v", accept, err)
	}
}

func nativeControlRoot(t *testing.T, dir, name string, ca *x509.Certificate, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nativeGuestFixture(t *testing.T, cert tls.Certificate, host string) (string, string, *atomic.Int64) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "native-guest-probe", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("native-guest-probe", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	t.Cleanup(func() { cancel(); _ = led.Close() })
	network, address := "tcp6", "[::1]:0"
	if host == "127.0.0.1" {
		network, address = "tcp4", "127.0.0.1:0"
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + net.JoinHostPort(host, port)
	store := invites.Store{Dir: dir}
	bearer, err := store.Mint("native-guest", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gate := &publicGate{
		store: store, origin: origin, rates: map[string]*inviteBucket{},
		service: &invites.Service{Store: store, Engine: eng},
	}
	next := gate.wrap(mcp.New(eng))
	requests := &atomic.Int64{}
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			next.ServeHTTP(w, r)
		}),
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}},
	}
	go func() { _ = srv.Serve(tls.NewListener(listener, srv.TLSConfig)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return origin + "/mcp", bearer, requests
}

// Inherit no bearer/provider/proxy/CA/plugin variables. Config directories are
// temporary. The process-level extra CA affects this probe alone, not macOS.
func nativeProbeEnv(dir, client, root string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + os.TempDir(),
		"CODEX_HOME=" + dir, "CLAUDE_CONFIG_DIR=" + dir, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "NO_PROXY=::1,localhost,127.0.0.1",
	}
	if root != "" {
		if client == "codex" {
			env = append(env, "CODEX_CA_CERTIFICATE="+root)
		} else {
			env = append(env, "NODE_EXTRA_CA_CERTS="+root)
		}
	}
	return env
}

func nativeInventory(t *testing.T, client, binary, root, url, bearer string) (bool, string, error) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if client == "claude" {
		add := exec.CommandContext(ctx, binary, "mcp", "add", "--scope", "local", "--transport", "http", "guest", url,
			"--header", "Authorization: Bearer "+bearer)
		add.Dir, add.Env = dir, nativeProbeEnv(dir, client, root)
		if out, err := add.CombinedOutput(); err != nil {
			return false, string(out), fmt.Errorf("isolated mcp add: %w", err)
		}
		cmd := exec.CommandContext(ctx, binary, "mcp", "list")
		cmd.Dir, cmd.Env = dir, nativeProbeEnv(dir, client, root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, string(out), fmt.Errorf("isolated mcp list: %w", err)
		}
		if !bytes.Contains(out, []byte("guest:")) {
			return false, string(out), fmt.Errorf("fixture MCP server absent from list")
		}
		return bytes.Contains(out, []byte("Connected")), string(bytes.TrimSpace(out)), nil
	}
	// Disabling plugins prevents the native app-server's automatic marketplace
	// clone from surviving process shutdown and racing fixture cleanup. It is
	// unrelated to MCP TLS verification, which remains enabled and unchanged.
	config := fmt.Sprintf("[features]\nplugins = false\nmcp_2026_07_28 = true\n[mcp_servers.guest]\nurl = %q\nstartup_timeout_sec = 8\nhttp_headers = { Authorization = %q }\n", url, "Bearer "+bearer)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		return false, "", err
	}
	cmd := exec.CommandContext(ctx, binary, "app-server")
	cmd.Dir, cmd.Env = dir, nativeProbeEnv(dir, client, root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, "", err
	}
	var stderr nativeProbeBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return false, "", err
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	enc := json.NewEncoder(stdin)
	if err := enc.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo":   map[string]string{"name": "dibs-native-tls-probe", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}}); err != nil {
		return false, "", err
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 4096), 1024*1024)
	initial, err := nativeReply(scan, 1)
	if err != nil {
		return false, stderr.String(), err
	}
	if string(initial["error"]) != "" {
		return false, string(initial["error"]), fmt.Errorf("app-server initialization refused")
	}
	if err := enc.Encode(map[string]any{"method": "initialized"}); err != nil {
		return false, "", err
	}
	if err := enc.Encode(map[string]any{"id": 2, "method": "mcpServerStatus/list", "params": map[string]any{
		"serverName": "guest", "detail": "toolsAndAuthOnly",
	}}); err != nil {
		return false, "", err
	}
	reply, err := nativeReply(scan, 2)
	if err != nil {
		return false, stderr.String(), err
	}
	if string(reply["error"]) != "" {
		return false, string(reply["error"]), fmt.Errorf("MCP inventory request refused")
	}
	var result struct {
		Data []struct {
			Name       string                     `json:"name"`
			Tools      map[string]json.RawMessage `json:"tools"`
			ToolsError *string                    `json:"toolsError"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply["result"], &result); err != nil {
		return false, string(reply["result"]), fmt.Errorf("fixture MCP inventory malformed: %w", err)
	}
	if len(result.Data) != 1 || result.Data[0].Name != "guest" {
		return false, string(reply["result"]), fmt.Errorf("fixture MCP inventory absent")
	}
	status := result.Data[0]
	detail := fmt.Sprintf("tools=%d error=%v", len(status.Tools), status.ToolsError)
	if status.ToolsError != nil {
		detail = "toolsError=" + *status.ToolsError
	}
	return status.ToolsError == nil && len(status.Tools) > 0, detail, nil
}

type nativeProbeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *nativeProbeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const limit = 128 * 1024
	if remaining := limit - b.b.Len(); remaining > 0 {
		_, _ = b.b.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (b *nativeProbeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func nativeReply(scanner *bufio.Scanner, id int) (map[string]json.RawMessage, error) {
	for scanner.Scan() {
		var reply map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(reply["id"])) == fmt.Sprint(id) {
			return reply, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("app-server reply %d missing: %w", id, err)
	}
	return nil, fmt.Errorf("app-server reply %d missing: %w", id, io.EOF)
}
