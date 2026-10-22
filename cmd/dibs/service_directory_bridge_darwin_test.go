// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

func TestPersistentBridgeFollowsServiceDirectoryWithoutLeakingCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tls    bool
		method string
	}{
		{"http", false, "tools/call"}, {"tls", true, "tools/call"}, {"stream", false, "subscriptions/listen"},
	} {
		t.Run(tc.name, func(t *testing.T) { serviceDirectorySwitch(t, tc.tls, tc.method) })
	}
}

func serviceDirectorySwitch(t *testing.T, secure bool, method string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	t.Setenv("DIBS_ADDR", "")
	var wrongSecret atomic.Int64
	board := func(name, secret string) string {
		t.Helper()
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Dibs-Local") != secret {
				wrongSecret.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var request struct {
				ID any `json:"id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error("setup request:", err)
				return
			}
			reply := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"board": name}}
			if method == "subscriptions/listen" {
				w.Header().Set("Content-Type", "text/event-stream")
				body, err := json.Marshal(reply)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply)
		}))
		dir := filepath.Join(home, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := fmt.Sprintf("addr = %q\n", srv.Listener.Addr().String())
		if secure {
			chain, ca := issuedChain(t, false)
			srv.TLS = &tls.Config{Certificates: []tls.Certificate{chain}, MinVersion: tls.VersionTLS12}
			if err := os.WriteFile(filepath.Join(dir, "trusted-certs.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0o600); err != nil {
				t.Fatal(err)
			}
			certFile, keyFile := filepath.Join(dir, "server-cert.pem"), filepath.Join(dir, "server-key.pem")
			key, err := x509.MarshalPKCS8PrivateKey(chain.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			for file, block := range map[string]*pem.Block{certFile: {Type: "CERTIFICATE", Bytes: chain.Certificate[0]}, keyFile: {Type: "PRIVATE KEY", Bytes: key}} {
				if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg += fmt.Sprintf("tls_cert = %q\ntls_key = %q\n", certFile, keyFile)
			srv.StartTLS()
		} else {
			srv.Start()
		}
		t.Cleanup(srv.Close)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	first := board(".dibs", strings.Repeat("a", 64))
	second := board("moved-board", strings.Repeat("b", 64))
	if err := paths.WriteServiceDirectory(first); err != nil {
		t.Fatal(err)
	}
	call := serviceDirectoryBridgeMethod(t, method)
	if got := call(1); resultBoard(got) != ".dibs" {
		t.Fatal("setup: first board was not reached:", got)
	}
	// Both endpoints remain alive. A refused-dial retry cannot repair this.
	if err := paths.WriteServiceDirectory(second); err != nil {
		t.Fatal(err)
	}
	got := call(2)
	if resultBoard(got) != "moved-board" || wrongSecret.Load() != 0 {
		t.Fatalf("bridge did not select target and credential from the same current directory: %v; foreign credential attempts=%d", got, wrongSecret.Load())
	}
}

func resultBoard(reply map[string]any) string {
	result, _ := reply["result"].(map[string]any)
	board, _ := result["board"].(string)
	return board
}

func serviceDirectoryBridge(t *testing.T) func(int) map[string]any {
	t.Helper()
	return serviceDirectoryBridgeMethod(t, "tools/call")
}

func serviceDirectoryBridgeMethod(t *testing.T, method string) func(int) map[string]any {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	exited := make(chan error, 1)
	go func() { exited <- runBridge(nil); close(exited) }()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("fixture bridge did not exit")
		}
		os.Stdin, os.Stdout = stdin, stdout
		_ = inR.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	replies := bufio.NewReader(outR)
	return func(id int) map[string]any {
		t.Helper()
		if _, err := fmt.Fprintf(inW, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":{"name":"board"}}`+"\n", id, method); err != nil {
			t.Fatal(err)
		}
		done := make(chan []byte, 1)
		go func() { line, _ := replies.ReadBytes('\n'); done <- line }()
		select {
		case line := <-done:
			var got map[string]any
			if err := json.Unmarshal(line, &got); err != nil {
				t.Fatalf("invalid bridge reply: %v %s", err, line)
			}
			return got
		case err := <-exited:
			t.Fatalf("bridge exited: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("fixture bridge did not reply")
		}
		return nil
	}
}
