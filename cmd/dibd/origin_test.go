package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// A page on another port of this machine is a different origin, and is refused.
//
// THE CSRF THIS CLOSES. The check read the hostname and ignored the port, so
// anything served from any port on this machine passed it. That is not a
// technicality: a cookie is scoped to a host and not a port, and SameSite
// =Strict does not separate them either, so a page on http://localhost:9999
// could send a credentialed POST carrying the board session. It needs no
// preflight to do it, because text/plain is CORS-safelisted and JSON travels
// inside it. CORS stops the attacker reading the reply and does nothing about
// the side effect, and the side effect reachable here is GrantRole.
//
// Any local process can bind a port, and on this machine that includes the
// agents this daemon coordinates. Found by a pre-release review.
func TestAPageOnAnotherLocalPortIsNotThisOrigin(t *testing.T) {
	gate := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")

	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"http://127.0.0.1:4777", true},
		{"http://localhost:4777", true},
		{"http://[::1]:4777", true},
		// The attack.
		{"http://localhost:9999", false},
		{"http://127.0.0.1:8080", false},
		{"https://localhost:4778", false},
		// Not loopback at all.
		{"https://example.com", false},
		{"http://localhost.evil.com:4777", false},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			if got := gate.localOrigin(tc.origin); got != tc.want {
				t.Errorf("localOrigin(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}

// And the gate actually refuses the request, not merely the predicate.
func TestTheGateRefusesAForeignLocalOrigin(t *testing.T) {
	gate := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
	reached := false
	h := gate.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodPost, "/roles", nil)
	req.Header.Set("Origin", "http://localhost:9999")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Error("a POST from a page on another local port reached the handler: with a " +
			"board session cookie attached, that is a role grant the operator never made")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
