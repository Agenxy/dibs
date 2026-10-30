// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func privateDir(t *testing.T) string {
	t.Helper()
	// macOS limits Unix socket names to 104 bytes. Testing.T.TempDir uses a
	// descriptive name whose pathname alone can exceed that kernel limit.
	dir, err := os.MkdirTemp("/tmp", "dibs-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestClientAuthenticatesIPCBeforeSendingAndKeepsLogicalAuthority(t *testing.T) {
	dir := privateDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var requests atomic.Int32
	srv := &http.Server{
		ReadHeaderTimeout: time.Second, ConnContext: ConnContext,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !IsLocal(r.Context()) || r.Header.Get("X-Dibs-Local") != "fixture-secret" || r.Host != "127.0.0.1:1" {
				t.Error("owner-authenticated request lost its local evidence, secret, or logical authority")
			}
			requests.Add(1)
			_, _ = io.WriteString(w, "accepted")
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	client := &http.Client{Transport: RoundTripper(dir, Local, nil), Timeout: time.Second}
	for range 2 {
		req, e := http.NewRequest(http.MethodPost, "https://127.0.0.1:1/mcp", strings.NewReader("fixture-body"))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Dibs-Local", "fixture-secret")
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.Request.URL.Scheme != "https" {
			t.Fatal("logical scheme changed")
		}
	}
	if requests.Load() != 2 {
		t.Fatal("setup: did not exercise live and pooled IPC")
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	if resp, err := client.Get("http://127.0.0.1:1/mcp"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a reused connection bypassed the now-unsafe directory")
	}
	if requests.Load() != 2 {
		t.Fatal("request bytes reached the socket after directory became unsafe")
	}
}

func TestUnsafeOrMissingIPCDoesNotSendAnythingToTCP(t *testing.T) {
	for _, state := range []string{"missing", "group-writable", "world-writable", "symlink", "ordinary-file", "socket-mode"} {
		t.Run(state, func(t *testing.T) {
			dir := privateDir(t)
			var seen atomic.Int32
			tcp := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { seen.Add(1) }))
			defer tcp.Close()
			var ln net.Listener
			if state != "missing" && state != "ordinary-file" {
				var err error
				ln, err = Listen(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = ln.Close() }()
			}
			switch state {
			case "group-writable":
				if err := os.Chmod(dir, 0o770); err != nil {
					t.Fatal(err)
				}
			case "world-writable":
				if err := os.Chmod(dir, 0o777); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(privateDir(t), "board")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				dir = alias
			case "ordinary-file":
				if err := os.WriteFile(Path(dir), []byte("not a socket"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket-mode":
				if err := os.Chmod(Path(dir), 0o666); err != nil {
					t.Fatal(err)
				}
			}
			client := &http.Client{Transport: RoundTripper(dir, Local, nil), Timeout: time.Second}
			req, err := http.NewRequest(http.MethodPost, tcp.URL+"/mcp", strings.NewReader("fixture-body"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Dibs-Local", "fixture-secret")
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
				t.Fatal("unsafe IPC was accepted")
			}
			if seen.Load() != 0 {
				t.Fatal("unsafe IPC fell back to the squatted TCP listener")
			}
		})
	}
}

func TestListenerPreservesReplacedSocketAndRefusesLiveEndpoint(t *testing.T) {
	dir := privateDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(dir); err == nil {
		t.Fatal("stole a live socket")
	}
	if err := os.Remove(Path(dir)); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Path(dir)); err != nil {
		t.Fatal("closing old listener removed replacement")
	}
}

func TestRemoteHTTPSNeverBecomesPlaintext(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { seen.Add(1) }))
	defer srv.Close()
	client := &http.Client{Transport: RoundTripper(privateDir(t), HTTPS, nil), Timeout: time.Second}
	if resp, err := client.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("HTTPS target downgraded")
	}
	if seen.Load() != 0 {
		t.Fatal("plaintext target received a request")
	}
	if IsLocal(context.Background()) {
		t.Fatal("unstamped context asserted local IPC")
	}
}

func TestPrivateDirectoryBelowWritableAncestorIsRefused(t *testing.T) {
	parent := privateDir(t)
	dir := filepath.Join(parent, "board")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, 0o700) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := Dial(ctx, dir)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "ancestor") {
		t.Fatalf("a private directory in a replaceable parent was accepted: %v", err)
	}
}

func TestAncestorAliasCannotHideAReplaceableLexicalParent(t *testing.T) {
	trusted := privateDir(t)
	dir := filepath.Join(trusted, "board")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := privateDir(t)
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(trusted, alias); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(privateDir(t), "outer")
	if err := os.Symlink(alias, nested); err != nil {
		t.Fatal(err)
	}
	spellings := []string{filepath.Join(alias, "board"), filepath.Join(nested, "board")}
	for _, spelling := range spellings {
		if err := CheckDir(spelling, false); err != nil {
			t.Fatalf("setup: a trusted ancestor alias was refused: %v", err)
		}
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, 0o700) }()
	for _, spelling := range spellings {
		if err := CheckDir(spelling, false); err == nil {
			t.Fatal("resolved private ancestry concealed a replaceable alias parent")
		}
	}
}

func TestProfileValidationMatchesThePathUsedForCredentialFiles(t *testing.T) {
	parent, target := privateDir(t), privateDir(t)
	for _, path := range []string{filepath.Join(target, "sub"), filepath.Join(target, "board")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	unsafe := filepath.Join(parent, "board")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "sub"), filepath.Join(parent, "alias")); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(parent, "alias") + "/../board"
	a, err := os.Stat(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(target, "board"))
	if err != nil || !os.SameFile(a, b) || filepath.Join(raw, "local.secret") != filepath.Join(unsafe, "local.secret") {
		t.Fatal("setup: raw directory and joined credential path do not demonstrate the different objects")
	}
	if err := CheckDir(raw, false); err == nil {
		t.Fatal("validated the raw symlink/.. target while credential paths use a different unsafe profile")
	}
}
