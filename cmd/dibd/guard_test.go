package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The bootstrap redemption endpoint trades a one-time token for a god-view
// session cookie and then redirects, stripping the token from the URL.
//
// It used to redirect to `r.URL` with the query cleared. For a server request
// r.URL carries only a path, so that looked safe, but a path beginning "//" is
// PROTOCOL-RELATIVE, and a browser reads "//evil.com/" as a different host. So
// a request to `//evil.com/?bt=<token>` would mint a fresh god-view session and
// then send the browser, cookie in hand, to somebody else's server.
//
// Found by running the project's own linter rather than by reading the code.
func TestBootstrapRedeemCannotRedirectOffHost(t *testing.T) {
	dir := t.TempDir()
	g := newAuthGate("test-secret", filepath.Join(dir, "admin.hash"))
	handler := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"//evil.com/", "///evil.com/", "//attacker"} {
		bt := g.mintBootstrap()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		req.URL.Path = path // set directly: httptest normalises some of these
		q := req.URL.Query()
		q.Set("bt", bt)
		req.URL.RawQuery = q.Encode()
		req.Header.Set("X-Dibs-Local", "test-secret")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		loc := rec.Header().Get("Location")
		if loc == "" {
			continue // not a redirect at all; nothing to escape with
		}
		if strings.HasPrefix(loc, "//") {
			t.Fatalf("path %q redirected to %q: protocol-relative, so the browser "+
				"leaves this host carrying a fresh god-view session", path, loc)
		}
		if !strings.HasPrefix(loc, "/") {
			t.Fatalf("path %q redirected to %q, not a rooted path", path, loc)
		}
	}
}

// The session cookie must be Secure whenever the connection is TLS, and must
// NOT be when it is not: a Secure cookie set over plain HTTP is never sent
// back, which would silently break the loopback board rather than protect it.
func TestSessionCookieIsSecureOnlyOverTLS(t *testing.T) {
	dir := t.TempDir()
	for _, overTLS := range []bool{false, true} {
		g := newAuthGate("test-secret", filepath.Join(dir, "admin.hash"))
		handler := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		bt := g.mintBootstrap()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/?bt="+bt, nil)
		req.Header.Set("X-Dibs-Local", "test-secret")
		if overTLS {
			req.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		var found *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == "dibs_session" {
				found = c
			}
		}
		if found == nil {
			t.Fatalf("tls=%v: no session cookie was set", overTLS)
		}
		if found.Secure != overTLS {
			t.Errorf("tls=%v: cookie Secure=%v, want %v", overTLS, found.Secure, overTLS)
		}
		if !found.HttpOnly || found.SameSite != http.SameSiteStrictMode {
			t.Errorf("tls=%v: cookie must stay HttpOnly+SameSite=Strict, got %+v", overTLS, found)
		}
	}
}

// Acting AS THE HUMAN needs the human's password, not the secret every agent
// already holds.
//
// /api/act/* and /api/me were absent from the god-view gate, so they fell to
// the coordination tier, which accepts the local secret alone, and every agent
// must hold that secret to call /mcp at all. Verified against a running daemon
// before the fix: an ordinary agent POSTed /api/act/join and /api/act/announce
// with nothing but the coordination secret and got 200 both times, and /api/me
// returned the operator's agent id.
//
// The announcement route is the sharpest: it creates an obligation on every
// other member of an agent, attributed to a human who never said it. The comment
// on godViewAuthorized promises "an agent holding the secret still lacks the
// password, so it cannot pass": that was not true of these routes.
//
// Raised by an independent reviewer (GPT-5.6-sol) reading the gate against the
// route table.
func TestActingAsTheHumanIsBehindTheAdminGate(t *testing.T) {
	for _, p := range []string{
		"/api/me",
		"/api/act/join", "/api/act/leave", "/api/act/post", "/api/act/announce",
		"/api/act/open", "/api/act/send", "/api/act/respond", "/api/act/ack",
		"/api/act/ack_announcement",
		// The mux cleans paths before dispatch, so the gate must clean too or
		// these reach the same handler having skipped it.
		"/api//act/post", "/x/../api/act/post", "/api/act/post/",
	} {
		if !godViewPath(p) {
			t.Errorf("%s can act as the operator and must require the admin password", p)
		}
	}

	// The coordination surface stays open to agents holding the secret,
	// gating it would break every agent on the board.
	for _, p := range []string{"/mcp", "/api/board", "/healthz"} {
		if godViewPath(p) {
			t.Errorf("%s is coordination, not the god view; gating it locks out every agent", p)
		}
	}

	// And the routes that were already gated stay gated.
	for _, p := range []string{"/", "/events", "/api/messages", "/api/admin/role", "/api/admin/prune"} {
		if !godViewPath(p) {
			t.Errorf("%s lost its gate", p)
		}
	}
}

// The gate is a PREFIX, and this test is the reason it stayed one.
//
// It used to enumerate the two admin routes that existed. That is correct until
// somebody adds a third, at which point the new route is reachable with the
// coordination secret alone (which every agent holds) and nothing anywhere
// says so. A hypothetical future route stands in for that third one.
func TestEveryAdminPathIsGatedIncludingOnesNotWrittenYet(t *testing.T) {
	for _, p := range []string{
		"/api/admin/role",
		"/api/admin/prune",
		"/api/admin/some-route-nobody-has-written-yet",
		// The ROOT, not just the subtree: path.Clean strips the trailing slash,
		// so "/api/admin/" arrives as "/api/admin" and a prefix test alone
		// misses it. A handler at that exact path would have failed open.
		"/api/admin",
		"/api/admin/",
		"/api//admin//role",    // slash variants must not dodge it
		"/x/../api/admin/role", // nor dot segments
		"/api/admin/role/",     // nor a trailing slash
	} {
		if !godViewPath(p) {
			t.Errorf("%s reaches an admin handler without the admin password", p)
		}
	}
	// And the gate must not swallow the coordination tier wholesale, or every
	// agent call starts demanding a password.
	for _, p := range []string{"/mcp", "/api/administrivia", "/healthz"} {
		if godViewPath(p) {
			t.Errorf("%s is not an admin path and must not need the password", p)
		}
	}
}
