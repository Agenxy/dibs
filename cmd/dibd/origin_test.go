package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
			if got := gate.localOrigin(tc.origin, "127.0.0.1:4777"); got != tc.want {
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

// A board served on a LAN or tailnet address accepts its own origin.
//
// THE REGRESSION THIS CATCHES, which the fix for the port-blind check
// introduced. `addr` may be a LAN or tailnet address so agents on other
// machines can reach the board, and Dibs documents that. Hardcoding the
// loopback hostnames made such a daemon reject its own board: navigation still
// loaded the page, because ordinary navigation sends no Origin, so it looked
// like it worked and every button returned 403. Tightening by pattern rather
// than by identity is what did it. Found by a pre-release review.
func TestABoardOnATailnetAddressAcceptsItsOwnOrigin(t *testing.T) {
	gate := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "100.72.14.3:4777")
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"https://100.72.14.3:4777", true},
		{"http://100.72.14.3:4777", true},
		// Still not anybody else.
		{"https://100.72.14.9:4777", false},
		{"https://100.72.14.3:9999", false},
		{"http://localhost:4777", false},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			if got := gate.localOrigin(tc.origin, "127.0.0.1:4777"); got != tc.want {
				t.Errorf("localOrigin(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}

	// Listening on every interface cannot narrow the host, and must not lock
	// the operator out of their own board by pretending it can.
	any := newAuthGate("s", filepath.Join(t.TempDir(), "admin.hash"), "0.0.0.0:4777")
	if !any.localOrigin("https://box.tail1234.ts.net:4777", "box.tail1234.ts.net:4777") {
		t.Error("a daemon listening on every interface refused an origin on its own port")
	}
	if any.localOrigin("https://box.tail1234.ts.net:9999", "box.tail1234.ts.net:4777") {
		t.Error("the port must still bind even when the host cannot")
	}
	// And a same-site SIBLING is not this board, which the earlier version of
	// this test blessed rather than checked.
	if any.localOrigin("https://evil.tail1234.ts.net:4777", "box.tail1234.ts.net:4777") {
		t.Error("a different hostname on the same port was accepted: SameSite does " +
			"not separate siblings, so that page carries the board's session")
	}
}

// The board refuses to be framed.
//
// THE CLICKJACK THIS CLOSES. The origin check stops a hostile page CALLING the
// daemon and does nothing about one EMBEDDING it. Cookies are host-scoped and
// not port-scoped, and different ports on one loopback hostname are same-site,
// so a page on http://localhost:9999 can frame the authenticated board on :4777
// with its twelve-hour session attached. Anything it induces a click on is
// issued by the board's OWN script, so the Origin is this daemon's and the
// check waves it through: overlay something plausible and the human approves a
// grant or an adoption they never read.
//
// Found by a pre-release review, which noted the foreign-origin test does not
// cover it, because framing is not a cross-origin request at all.
func TestTheBoardCannotBeFramed(t *testing.T) {
	gate := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
	h := gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>the board</html>"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'. Without "+
			"it a page on another local port frames the authenticated board and "+
			"drives it with the human's own session", csp)
	}
}
