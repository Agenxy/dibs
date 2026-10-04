package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/testport"
)

func TestDirectIPListenerActuallyServesTLSAndOnlyInvitations(t *testing.T) {
	dir := t.TempDir()
	fleetCert, _, err := ensureSelfSignedCert(dir, "127.0.0.1:4777")
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := os.ReadFile(fleetCert)
	if err != nil {
		t.Fatal(err)
	}
	fleetCA, err := os.ReadFile(filepath.Join(dir, "tls-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "guest-tls-test", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("guest-tls-test", core.DefaultLimits()), led, nil)
	ctx, stop := context.WithCancel(context.Background())
	go eng.Run(ctx)
	t.Cleanup(func() { stop(); _ = led.Close() })
	// Loopback is the hermetic fixture, not an operator-advertisable global IP.
	cfg := publicConfig{IP: netip.MustParseAddr("::1")}
	// Real near-expiry signer: every replacement leaf is renewal-due. A fake
	// leaf deadline would reset on the first renewal and miss the failure.
	ca, signer, err := ensureGuestCA(dir, cfg.IP)
	if err != nil {
		t.Fatal(err)
	}
	ca.NotAfter = time.Now().Add(30 * time.Minute)
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &signer.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePEM(filepath.Join(dir, "guest-ca.pem"), "CERTIFICATE", caDER, 0o644); err != nil {
		t.Fatal(err)
	}
	var fail <-chan error
	var closePublic func()
	addr := testport.Bind(t, "tcp6", "[::1]:0", func(addr string) {
		cfg = publicConfig{URL: "https://" + addr, Addr: addr, IP: netip.MustParseAddr("::1")}
		if err := preparePublic(&cfg, dir); err != nil {
			t.Fatal(err)
		}
		// Enter through the real listener with a short fixture clock.
		cfg.guestPoll = 20 * time.Millisecond
	}, func() error {
		var err error
		fail, closePublic, err = startPublic(ctx, cfg, dir, eng, "never-public-secret", stop, nil)
		return err
	})
	t.Cleanup(func() {
		closePublic()
		select {
		case err := <-fail:
			t.Error(err)
		default:
		}
	})
	// The listener must terminate TLS itself, not rely on a fixture proxy.
	conn, err := tls.Dial("tcp", addr, &tls.Config{MinVersion: tls.VersionTLS13})
	if err == nil {
		_ = conn.Close()
		t.Fatal("guest root was silently trusted by the system store")
	}
	pemBytes, err := os.ReadFile(filepath.Join(dir, "guest-ca.pem"))
	if err != nil {
		t.Fatalf("listener did not prepare its separate guest CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("guest CA is not PEM")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	t.Cleanup(client.CloseIdleConnections)
	service := &invites.Service{Engine: eng, Store: invites.Store{Dir: dir}, URL: cfg.URL, Endpoint: cfg.Endpoint}
	minted, err := service.Human(ctx, "mint", "another-guest", "", 3600, false)
	if err != nil {
		t.Fatalf("private issuer could not mint guest trust recipe: %v", err)
	}
	recipe, ok := minted["config"].(map[string]any)
	if !ok || recipe["ca_pem"] != string(pemBytes) || recipe["ca_spki_sha256"] != cfg.Endpoint.Snapshot().CASPKIPin {
		t.Fatalf("private invitation omitted guest TLS metadata: %v", minted)
	}
	if _, present := recipe["codex_toml"]; present {
		t.Fatal("private invitation offered an unverified native adapter")
	}
	key, err := (invites.Store{Dir: dir}).Mint("guest-worker", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, bearer, local string
		status              int
	}{
		{"/mcp", key, "", http.StatusOK},
		{"/mcp", "", "", http.StatusUnauthorized},
		{"/mcp", key, "never-public-secret", http.StatusUnauthorized},
		{"/api/board", key, "", http.StatusForbidden},
		{"/api/guest-status", key, "", http.StatusForbidden},
		{"/mcp?q=1", key, "", http.StatusForbidden},
	} {
		req, err := http.NewRequest(http.MethodPost, cfg.URL+test.path, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if test.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+test.bearer)
		}
		if test.local != "" {
			req.Header.Set("X-Dibs-Local", test.local)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("TLS door %s: %v", test.path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != test.status {
			t.Fatalf("%s got %d, want %d", test.path, resp.StatusCode, test.status)
		}
		if resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
			t.Fatal("guest listener did not negotiate TLS1.3")
		}
	}
	if err := (invites.Store{Dir: dir}).Revoke("guest-worker"); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, cfg.URL+"/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("revoked key survived direct TLS door")
	}
	for path, before := range map[string][]byte{fleetCert: fleet, filepath.Join(dir, "tls-ca.pem"): fleetCA} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("guest TLS changed fleet identity %s: %v", path, err)
		}
	}
	// A due renewal whose durable signer is unreadable must withdraw the real
	// endpoint and recipe, not silently rotate roots or stop the private engine.
	if err := os.WriteFile(filepath.Join(dir, "guest-ca.pem"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for cfg.Endpoint.Snapshot().Reason == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	info := cfg.Endpoint.Snapshot()
	if info.Reason == "" || info.URL != "" || info.CAPEM != "" {
		t.Fatalf("failed renewal left guest recipe available: %+v", info)
	}
	if _, err := service.Human(ctx, "mint", "withdrawn-guest", "", 3600, false); err == nil {
		t.Fatal("private issuer minted a stale guest endpoint after actual listener withdrawal")
	}
	if ctx.Err() != nil {
		t.Fatal("guest renewal failure stopped private board")
	}
	client.CloseIdleConnections()
	if response, err := client.Get(cfg.URL + "/mcp"); err == nil {
		_ = response.Body.Close()
		t.Fatal("withdrawn listener still accepts TLS")
	}
}
