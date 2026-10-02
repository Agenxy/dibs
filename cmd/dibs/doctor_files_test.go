package main

import (
	"crypto/tls"
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
