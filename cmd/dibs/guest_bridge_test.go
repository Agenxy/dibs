// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/selfupdate"
)

type guestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f guestRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGuestTransportScopeEveryRequest(t *testing.T) {
	for _, mode := range []string{"good", "GET", "query", "path", "host", "local secret", "local nonce", "expired"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			g := &guestTransport{
				endpoint: "https://[::1]:1234/mcp", credential: "invitation-only", expires: time.Now().Add(time.Hour),
				next: guestRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Authorization") != "Bearer invitation-only" {
						t.Fatal("invitation credential was not the sole bearer source")
					}
					versions := r.Header.Values(selfupdate.GuestVersionHeader)
					if len(versions) != 1 || versions[0] != build.Version {
						t.Fatal("guest compatibility declaration did not override caller with actual build")
					}
					return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
				}),
			}
			r, err := http.NewRequestWithContext(context.Background(), http.MethodPost, g.endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer caller-override")
			r.Header.Add(selfupdate.GuestVersionHeader, "99.0.0")
			r.Header.Add(selfupdate.GuestVersionHeader, "99.0.1")
			switch mode {
			case "GET":
				r.Method = http.MethodGet
			case "query":
				r.URL.RawQuery = "secret=escape"
			case "path":
				r.URL.Path = "/other"
			case "host":
				r.Host = "another.example"
			case "local secret":
				r.Header.Set("X-Dibs-Local", "must-not-send")
			case "local nonce":
				r.Header.Set("X-Dibs-Agent-Nonce", "must-not-send")
			case "expired":
				g.expires = time.Now().Add(-time.Second)
			}
			response, rerr := g.RoundTrip(r)
			if len(r.Header.Values(selfupdate.GuestVersionHeader)) != 2 || r.Header.Get("Authorization") != "Bearer caller-override" {
				t.Fatal("transport altered caller-owned headers")
			}
			err = rerr
			if response != nil {
				_ = response.Body.Close()
			}
			if mode == "good" {
				if err != nil || calls != 1 {
					t.Fatalf("valid request failed: %v, calls %d", err, calls)
				}
			} else if err == nil || calls != 0 {
				t.Fatalf("scope escaped on %s: %v, calls %d", mode, err, calls)
			}
		})
	}
}

func TestGuestForwardPreservesIDsAndUncertainOutcome(t *testing.T) {
	const request = `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"send"}}`
	for _, body := range []string{
		"", "<html>proxy</html>", `{"error":"not an RPC reply"}`,
		`{"jsonrpc":"2.0","id":9007199254740993}`,
		`{"jsonrpc":"2.0","id":9007199254740993,"result":{},"error":{}}`,
		`{"jsonrpc":"2.0","id":9007199254740992,"result":{}}`,
	} {
		calls := 0
		client := &http.Client{Transport: guestRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		r, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://[::1]:1234/mcp",
			strings.NewReader(request))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		writer := &syncWriter{w: bufio.NewWriter(&out)}
		guestForward(client, r, []byte(request), writer)
		writer.flush()
		var rpc struct {
			ID    json.RawMessage `json:"id"`
			Error any             `json:"error"`
		}
		if err := json.Unmarshal(out.Bytes(), &rpc); err != nil || string(rpc.ID) != "9007199254740993" ||
			rpc.Error == nil || calls != 1 {
			t.Fatalf("malformed reply changed ID, faked success or retried mutation: %v calls=%d body=%s", err, calls, out.Bytes())
		}
		if strings.Contains(out.String(), "dibs doctor") || strings.Contains(out.String(), "dibs upgrade") {
			t.Fatal("guest diagnostic sent the agent to operate a different local board")
		}
	}
}

func TestGuestNonceDoesNotAlterCallerOrOpaqueNumbers(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call",` +
		`"params":{"name":"register","arguments":{"name":"guest"},"_meta":{"opaque":9007199254740995}}}`)
	out := guestRecoveryNonce(line, "recipe-nonce")
	if !bytes.Contains(out, []byte(`"id":9007199254740993`)) ||
		!bytes.Contains(out, []byte(`"opaque":9007199254740995`)) || !bytes.Contains(out, []byte(`"nonce":"recipe-nonce"`)) {
		t.Fatal("nonce injection damaged request ID or metadata")
	}
	for _, nonce := range []string{`"caller-nonce"`, `""`, `null`} {
		given := bytes.Replace(line, []byte(`"name":"guest"`), []byte(`"name":"guest","nonce":`+nonce), 1)
		if !bytes.Equal(guestRecoveryNonce(given, "recipe-nonce"), given) {
			t.Fatal("caller nonce was replaced rather than left for server validation")
		}
	}
}

func TestGuestNonceRefusalNeverRendersFilesystemErrors(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call"}`)
	private := "/private/guest-store/secret-path with nonce-material-canary"
	reply := guestNonceReply(line, errors.New(private))
	if !bytes.Contains(reply, []byte(`"id":9007199254740993`)) || bytes.Contains(reply, []byte(private)) ||
		bytes.Contains(reply, []byte("doctor")) || bytes.Contains(reply, []byte("upgrade")) {
		t.Fatal("nonce-store refusal lost its ID, rendered a filesystem error or offered local-board remedies")
	}
	if len(guestNonceReply([]byte(`{"jsonrpc":"2.0","method":"tools/call"}`), errors.New(private))) != 0 {
		t.Fatal("nonce-store refusal synthesized a notification reply")
	}
}

func TestGuestLegacyStoreFailsClosedAndIsScoped(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "invitation.json")
	r := &guestRecipe{Name: "guest", Endpoint: "https://[::1]:1234/mcp", Pin: "fixture-pin"}
	first, err := guestStoredNonce(file, r)
	if err != nil || len(first) != 64 {
		t.Fatalf("new nonce not durably stored: %v", err)
	}
	second, err := guestStoredNonce(file, r)
	if err != nil || second != first {
		t.Fatalf("retained nonce changed: %v", err)
	}
	r.Name = "another"
	third, err := guestStoredNonce(file, r)
	if err != nil || third == first {
		t.Fatalf("different invitation identity reused a nonce: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.nonce"))
	if err != nil || len(files) != 2 {
		t.Fatalf("store setup incomplete: %v", err)
	}
	for _, path := range files {
		if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := guestStoredNonce(file, r); err == nil {
		t.Fatal("corrupt credential was replaced with a fresh sibling nonce")
	}
}
