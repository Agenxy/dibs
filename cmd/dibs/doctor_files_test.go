// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoctorMeasuresPublicTLSWithoutCredentials(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(tls.VersionName(version), func(t *testing.T) {
			s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Dibs-Local") != "" {
					t.Error("public measurement leaked a credential")
				}
				if r.Method != http.MethodHead || r.URL.Path != "/mcp" {
					t.Error("not a harmless MCP availability probe")
				}
				w.WriteHeader(http.StatusForbidden) // TLS is measurable without invitation authority
			}))
			s.TLS = &tls.Config{MinVersion: version, MaxVersion: version}
			s.StartTLS()
			defer s.Close()
			var good, bad string
			probePublicTLS(s.Client(), s.URL, func(msg string) { good = msg }, func(msg, hint string) { bad = msg + hint })
			if version == tls.VersionTLS12 && (!strings.Contains(bad, "below TLS 1.3") || good != "") {
				t.Fatalf("TLS 1.2 falsely accepted: good=%s bad=%s", good, bad)
			}
			if version == tls.VersionTLS13 && (good == "" || bad != "") {
				t.Fatalf("TLS 1.3 not confirmed: good=%s bad=%s", good, bad)
			}
		})
	}
}

func TestDoctorReportsWithdrawnGuestEndpointWithoutStaleTLSProbe(t *testing.T) {
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dibs-Local") != "private-proof" {
			t.Error("guest status queried without board proof")
		}
		switch r.URL.Path {
		case "/api/guest-status":
			_ = json.NewEncoder(w).Encode(map[string]string{"mode": "direct-ip", "address": "2600:1700::1", "stability": "operator-asserted", "withdrawn_reason": "address disappeared"})
		case "/api/transfer-status":
			t.Error("withdrawn guest status fell back to stale transfer origin")
		default:
			t.Errorf("unexpected probe %s", r.URL.Path)
		}
	}))
	defer board.Close()
	t.Setenv("DIBS_ADDR", board.URL)
	var warnings []string
	checkPublicTLS(board.Client(), "private-proof", func(string) { t.Error("withdrawn endpoint reported healthy") }, func(msg, hint string) { warnings = append(warnings, msg+" "+hint) })
	if len(warnings) != 1 || !strings.Contains(warnings[0], "withdrawn") || !strings.Contains(warnings[0], "address disappeared") {
		t.Fatalf("withdrawal not reported: %v", warnings)
	}
}
