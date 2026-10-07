// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
// This identifier applies only to Agenxy-authored portions.
// Outside contributions retain their original licences; see NOTICE.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/transfer"
)

func TestFileCLIUsesRealMCPAndVerifiedBytes(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "cli-transfer", box)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(core.NewState("cli-transfer", core.DefaultLimits()), led, nil)
	e.SetBlobs(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = led.Close() })
	registered, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "cli-worker"})
	if err != nil || registered["token"] == nil {
		t.Fatalf("setup: %v %v", registered, err)
	}
	files := transfer.New(ctx, e, store, dir)
	api := mcp.New(e)
	api.SetTransfers(files, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Dibs-Local") != "" {
				t.Error("byte request carried board authority")
			}
			files.Handler("", false).ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Dibs-Local") != strings.Repeat("a", 64) {
			t.Error("MCP credential missing")
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	awaitEnv(t, strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("DIBS_TOKEN", registered["token"].(string))
	t.Setenv("DIBS_INVITE", "")
	plain := bytes.Repeat([]byte("CLI transfer artifact"), 12000)
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return filesCmd("put", []string{source, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var stored fileResult
	if err := json.Unmarshal([]byte(out), &stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain)
	if stored.Blob != "sha256:"+hex.EncodeToString(sum[:]) || stored.Size != int64(len(plain)) || strings.Contains(out, "/files/") {
		t.Fatalf("put did not print a verified non-secret result: %s", out)
	}
	destination := filepath.Join(t.TempDir(), "download.bin")
	out, err = captureStdout(t, func() error { return filesCmd("get", []string{stored.Blob, "-o", destination, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(out, "/files/") {
		t.Fatal("download stdout disclosed capability")
	}
	if _, err := getFile(stored.Blob, destination); err == nil {
		t.Fatal("get overwrote existing destination")
	}
	out, err = captureStdout(t, func() error { return filesCmd("get", []string{"not-a-blob", "--json"}) })
	var domain core.Error
	if err == nil || json.Unmarshal([]byte(out), &domain) != nil || domain.Code == "" || domain.Hint == "" {
		t.Fatalf("JSON error/exit contract: %q %v", out, err)
	}
}

func TestCLIDownloadResumesAndNeverPublishesBadHash(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			plain := bytes.Repeat([]byte("bounded retry"), 20000)
			sum := sha256.Sum256(plain)
			blob := "sha256:" + hex.EncodeToString(sum[:])
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("unexpected method")
				}
				if calls.Add(1) == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nETag: %q\r\n\r\n", len(plain), blob)
					_, _ = conn.Write(plain[:75001])
					return
				}
				if r.Header.Get("Range") != "bytes=75001-" || r.Header.Get("If-Range") != `"`+blob+`"` {
					t.Error("resume lost accepted prefix")
				}
				body := plain
				if corrupt {
					body = append([]byte(nil), plain...)
					body[len(body)-1] ^= 1
				}
				w.Header().Set("ETag", `"`+blob+`"`)
				http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
			}))
			defer server.Close()
			awaitEnv(t, strings.TrimPrefix(server.URL, "http://"))
			f, err := os.CreateTemp(t.TempDir(), "download-*")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			size, err := downloadFile(f, blob, fileDescriptor{URL: server.URL + "/files/test"})
			if corrupt {
				if err == nil {
					t.Fatal("corrupt resumed download accepted")
				}
				return
			}
			if err != nil || size != int64(len(plain)) {
				t.Fatalf("resume: %d %v", size, err)
			}
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(f)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatal("resumed bytes differ")
			}
		})
	}
}

func TestFileDescriptorRefusesOtherOriginsAndSecretsInErrors(t *testing.T) {
	awaitEnv(t, "127.0.0.1:12345")
	for _, target := range []string{"http://example.com/files/key", "http://127.0.0.1:12346/files/key", "http://user:secret@127.0.0.1:12345/files/key"} {
		if err := validateFileURL(target); err == nil || strings.Contains(err.Error(), target) {
			t.Fatalf("unsafe descriptor/error: %s %v", target, err)
		}
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:12345/files/secret-capability", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := byteClient()
	defer client.CloseIdleConnections()
	resp, err := requestBytes(client, req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || strings.Contains(err.Error(), "secret-capability") {
		t.Fatalf("secret URL in network error: %v", err)
	}
}
