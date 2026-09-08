package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A whitespace-excluding pattern read `/Users/Example User/bin/dibd` as
// `/bin/dibd`, so a unit naming the installed binary looked like one pinning a
// different build and recovery abandoned a correct service unit for an
// unsupervised process. The directory check beside it has always handled
// spaces; the executable check must too.
func TestAUnitWhoseBinaryPathHasSpacesIsReadWhole(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "Example User", "bin", "dibd")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	board := filepath.Join(dir, "Fleet Review", ".dibs")
	if err := os.MkdirAll(board, 0o755); err != nil {
		t.Fatal(err)
	}

	plist := filepath.Join(t.TempDir(), "org.agenxy.dibs.plist")
	body := "<plist><dict><key>ProgramArguments</key><array>" +
		"<string>" + installed + "</string><string>-dir</string><string>" + board + "</string>" +
		"</array></dict></plist>"
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := unitBinary(plist); got != installed {
		t.Fatalf("unitBinary read %q, want the whole pinned path %q: a path with a space in it "+
			"was truncated, so a correct unit reads as pinning another build", got, installed)
	}
	// And the decision that rests on it: this unit is fit to restart.
	if reason := unitUnfitToRestart(plist, board, installed); reason != "" {
		t.Fatalf("a unit naming the installed binary and this board was judged unfit (%q): "+
			"recovery would abandon a correct service unit and start an unsupervised process", reason)
	}
}

// A refusal is answered "nothing was applied"; a reply the daemon began and
// could not finish is answered "this may or may not have been applied",
// because the op may already be ledgered. Telling a caller its send was
// refused before it was read invites a retry that duplicates the message.
func TestADamagedReplyDoesNotClaimTheRequestWasRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"a 200 cut short mid-JSON", http.StatusOK, `{"jsonrpc":"2.0","id":7,"result":{"content":[{"te`},
		{"an empty 500", http.StatusInternalServerError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var buf bytes.Buffer
			out := &syncWriter{w: bufio.NewWriter(&buf)}
			line := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"send"}}`)
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			forward(srv.Client(), req, line, out, nil)
			got := strings.TrimSpace(buf.String())
			var reply struct {
				Error struct {
					Message string `json:"message"`
					Data    struct {
						Hint string `json:"hint"`
					} `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(got), &reply); err != nil {
				t.Fatalf("the harness read %q, which is not JSON-RPC: %v", got, err)
			}
			if strings.Contains(reply.Error.Data.Hint, "before reading it") {
				t.Errorf("a damaged reply was reported as a refusal decided before the request "+
					"was read: the op may already be ledgered, and this advice invites a retry "+
					"that duplicates it. hint=%q", reply.Error.Data.Hint)
			}
			if !strings.Contains(reply.Error.Data.Hint, "may or may not have been applied") {
				t.Errorf("the hint does not say the outcome is unknown: %q", reply.Error.Data.Hint)
			}
			if !strings.Contains(reply.Error.Data.Hint, "op_id") {
				t.Errorf("the hint does not name the idempotent retry: %q", reply.Error.Data.Hint)
			}
		})
	}
}

// A 5xx carrying a text body is not a gate refusal. A proxy's 502 or 504 can
// arrive after the daemon committed the operation, so promising it was
// refused before being read invites a retry that duplicates a send.
func TestATextBodiedServerErrorDoesNotPromiseNothingWasApplied(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantUnk bool
	}{
		{"a 502 with an HTML body", http.StatusBadGateway, "<html><body>Bad Gateway</body></html>", true},
		{"a 504 with plain text", http.StatusGatewayTimeout, "upstream timed out", true},
		// A 4xx IS decided before the request is read, and must keep saying so.
		{"a 401 from the gate", http.StatusUnauthorized, "unauthorized", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var buf bytes.Buffer
			out := &syncWriter{w: bufio.NewWriter(&buf)}
			line := []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"send"}}`)
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			forward(srv.Client(), req, line, out, nil)
			var reply struct {
				Error struct {
					Data struct {
						Hint string `json:"hint"`
					} `json:"data"`
				} `json:"error"`
			}
			got := strings.TrimSpace(buf.String())
			if err := json.Unmarshal([]byte(got), &reply); err != nil {
				t.Fatalf("the harness read %q, which is not JSON-RPC: %v", got, err)
			}
			unknown := strings.Contains(reply.Error.Data.Hint, "may or may not have been applied")
			if tc.wantUnk && !unknown {
				t.Errorf("a %d was reported as refused before the request was read, but a proxy "+
					"can answer that after the daemon committed: a retry without op_id duplicates "+
					"the send. hint=%q", tc.status, reply.Error.Data.Hint)
			}
			if !tc.wantUnk && unknown {
				t.Errorf("a %d is decided before the request is read and must still say nothing "+
					"was applied. hint=%q", tc.status, reply.Error.Data.Hint)
			}
		})
	}
}
