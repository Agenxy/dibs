// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
)

func TestAgentCallsRetainTransportRefusalGuidance(t *testing.T) {
	for _, kind := range []string{"local", "https"} {
		t.Run(kind, func(t *testing.T) {
			dir := cliIPCProfile(t)
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("private-fixture-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DIBS_DIR", dir)
			t.Setenv("DIBS_ADDR", "http://127.0.0.1:1")
			t.Setenv("DIBS_TRANSPORT", kind)
			t.Setenv("DIBS_INVITE", "")
			reresolveMCPEndpoint()
			t.Cleanup(reresolveMCPEndpoint)
			var result any
			err := agentToolCall("register", map[string]any{"nonce": "private-fixture-nonce"}, &result)
			want := localtransport.MissingHint
			if kind == "local" && !localtransport.Supported() {
				want = localtransport.UnsupportedHint
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("transport refusal lost its corrective guidance: %v; want %q", err, want)
			}
			for _, private := range []string{"private-fixture-secret", "private-fixture-nonce", "http://127.0.0.1:1"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("transport guidance disclosed private request material: %v", err)
				}
			}
		})
	}
}

func TestLocalMCPConfigDoesNotRecommendPlaintextRemoteCredentials(t *testing.T) {
	dir := cliIPCProfile(t)
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("private-fixture-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "http://127.0.0.1:4777")
	t.Setenv("DIBS_TRANSPORT", "local")
	out, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal("local IPC configuration failed:", err)
	}
	_, remote, found := strings.Cut(out, "Agents on ANOTHER machine")
	if !found || !strings.Contains(remote, "verified HTTPS") || !strings.Contains(remote, "HTTPS SSH forward") {
		t.Fatalf("local recipe lost the authenticated remote migration: %s", out)
	}
	if strings.Contains(remote, `"DIBS_ADDR": "http://`) || strings.Contains(out, "private-fixture-secret") {
		t.Fatalf("configuration recommended or printed plaintext remote authority: %s", out)
	}
}

func TestBridgeClientCloseIdleConnectionsReleasesItsIPCConnection(t *testing.T) {
	if !localtransport.Supported() {
		t.Skip("kernel-authenticated Unix connection lifecycle")
	}
	dir := cliIPCProfile(t)
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "http://127.0.0.1:1")
	t.Setenv("DIBS_TRANSPORT", "local")
	ln, err := localtransport.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	closed := make(chan struct{})
	var once sync.Once
	srv := &http.Server{
		ReadHeaderTimeout: time.Second, ConnContext: localtransport.ConnContext,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				once.Do(func() { close(closed) })
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	route, err := newBridgeRoute()
	if err != nil {
		t.Fatal(err)
	}
	client := bridgeDaemonClient(route, time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://127.0.0.1:1/mcp")
	if err != nil {
		t.Fatal("setup: bridge client never reached its socket:", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the bridge client's idle pool left its authenticated socket connected")
	}
}

func TestInvitationCredentialsDoNotTrustAReplaceableProfileAlias(t *testing.T) {
	if !localtransport.Supported() {
		t.Skip("Unix private-profile ancestry")
	}
	seen := make(chan struct{}, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-invitation-fixture" ||
			r.Header.Get("X-Dibs-Local") != "" || r.Header.Get("X-Dibs-Admin") != "" {
			t.Error("invitation request acquired or lost credential authority")
		}
		seen <- struct{}{}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)
	}))
	defer srv.Close()
	trusted := cliIPCProfile(t)
	dir := filepath.Join(trusted, "board")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	trustDiagnosticServer(t, dir, srv)
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", srv.URL)
	t.Setenv("DIBS_INVITE", "private-invitation-fixture")
	reresolveMCPEndpoint()
	t.Cleanup(reresolveMCPEndpoint)
	var result map[string]any
	if err := agentToolCall("register", map[string]any{}, &result); err != nil || result["ok"] != true {
		t.Fatalf("setup: verified invitation TLS failed: %v %v", err, result)
	}
	if len(seen) != 1 {
		t.Fatal("setup: invitation positive control never reached TLS")
	}
	parent := cliIPCProfile(t)
	if err := os.Symlink(trusted, filepath.Join(parent, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, 0o700) }()
	t.Setenv("DIBS_DIR", filepath.Join(parent, "alias", "board"))
	reresolveMCPEndpoint()
	if err := agentToolCall("register", map[string]any{}, &result); err == nil || len(seen) != 1 {
		t.Fatalf("invitation accepted replaceable TLS trust: err=%v, %d credential-bearing requests", err, len(seen))
	}
}

func TestLegacyInvitedForwardNeedsNoLocalBoardOrCoordinationSecret(t *testing.T) {
	seen := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-invitation-fixture" ||
			r.Header.Get("X-Dibs-Local") != "" || r.Header.Get("X-Dibs-Admin") != "" {
			t.Error("legacy invited forward changed credential scope")
		}
		seen <- struct{}{}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)
	}))
	defer srv.Close()
	t.Setenv("DIBS_DIR", filepath.Join(cliIPCProfile(t), "no-local-board"))
	t.Setenv("DIBS_ADDR", srv.URL)
	t.Setenv("DIBS_INVITE", "private-invitation-fixture")
	reresolveMCPEndpoint()
	t.Cleanup(reresolveMCPEndpoint)
	var result map[string]any
	if err := agentToolCall("register", map[string]any{}, &result); err != nil || result["ok"] != true || len(seen) != 1 {
		t.Fatalf("documented legacy invitation route was broken: %v %v", err, result)
	}
}
