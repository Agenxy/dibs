// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/boardconfig"
)

func TestBoardFingerprintOnlyOnLocalCoordinationRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, bind, path string
		want             bool
	}{
		{"ipv4", "127.0.0.1:4777", "/mcp", true},
		{"ipv6", "[::1]:4777", "/mcp", true},
		{"wildcard", "0.0.0.0:4777", "/mcp", false},
		{"lan", "192.0.2.1:4777", "/mcp", false},
		{"human board", "127.0.0.1:4777", "/", false},
		{"liveness", "127.0.0.1:4777", "/livez", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := newAuthGate("private-secret", "", tc.bind)
			gate.boardFingerprint = boardconfig.BoardFingerprint("raw-machine-node")
			handler := gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok\n"))
			}))
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "http://"+tc.bind+tc.path, nil))
			got := resp.Header().Get("X-Dibs-Board")
			if tc.want {
				if resp.Code != http.StatusUnauthorized || got != gate.boardFingerprint {
					t.Fatalf("local refusal: status %d, fingerprint %q", resp.Code, got)
				}
				if resp.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("identity refusal must not be cached")
				}
			} else if got != "" {
				t.Fatalf("identity disclosed outside local coordination refusal: %q", got)
			}
		})
	}
}

func TestPublicInvitationRefusalDisclosesNoBoardFingerprint(t *testing.T) {
	f := newCloudFixture(t)
	req, err := http.NewRequest(http.MethodPost, f.public.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.public.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("setup: expected public authentication refusal, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Dibs-Board"); got != "" {
		t.Fatalf("public invitation listener disclosed board fingerprint: %q", got)
	}
}
