package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	g := newAuthGate("test-secret", filepath.Join(dir, "admin.hash"), "127.0.0.1:4777")
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
		g := newAuthGate("test-secret", filepath.Join(dir, "admin.hash"), "127.0.0.1:4777")
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
			if c.Name == g.sessionCookieName() {
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

// Liveness must not require the coordination secret.
//
// Everything else on this daemon does, correctly: it reveals the board. "Is the
// process up" does not, and gating it meant the only way to supervise Dibs was
// to hand a monitoring system the same secret every agent authenticates with.
// An operator evaluating Dibs raised it, and the workaround they named is why
// it matters: the secret spreads to something with no business holding it.
//
// Driven through the real gate rather than a helper, because the question is
// what an unauthenticated request actually gets.
func TestLivenessIsReachableWithoutTheSecret(t *testing.T) {
	gate := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
	served := gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	}))

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/livez", want: http.StatusOK},
		// Path variants must resolve to the same decision, not slide past it.
		{path: "/livez/", want: http.StatusOK},
		{path: "/x/../livez", want: http.StatusOK},
		// The surfaces that DO reveal the board stay closed.
		{path: "/api/board", want: http.StatusUnauthorized},
		{path: "/mcp", want: http.StatusUnauthorized},
		{path: "/api/logs", want: http.StatusUnauthorized},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			served.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("GET %s without the secret = %d, want %d", tc.path, rec.Code, tc.want)
			}
		})
	}
}

// A stolen session cookie must not grant a role from outside a browser.
//
// Cookies are host-scoped and not port-scoped, and every port on one loopback
// hostname is the same site, so SameSite=Strict does not separate them. A second
// server on 127.0.0.1 therefore receives dibs_session as soon as the operator
// visits it, and can replay it server-side, where no Origin header is sent and
// the check in wrap() is skipped by construction. That reached /api/admin/role.
// Demonstrated by the pre-release review with a cookie jar and a handler-level
// replay.
//
// Browsers send Origin on state-changing methods; a replay does not.
func TestAStolenSessionCannotChangeStateWithoutABrowser(t *testing.T) {
	// Configured with its own address, as a real daemon is: without it
	// localOrigin falls back to "any loopback host", which accepts the sibling
	// port this test is about. That was the fixture, not the product.
	g := &authGate{
		sessions: map[string]boardSession{}, boot: map[string]time.Time{},
		host: "127.0.0.1", port: "4777",
	}
	const token, pageKey = "stolen-session-value", "the-page-key-that-never-left-the-tab"
	g.sessions[token] = boardSession{exp: time.Now().Add(time.Hour), page: pageKey}

	// The THIEF: it holds the cookie, because cookies are host-scoped and the
	// operator visited it on another port. It writes its own headers, because it
	// is not a browser, so it declares whatever Origin it likes.
	thief := func(method, target string) *http.Request {
		r := httptest.NewRequest(method, "http://127.0.0.1:4777"+target, nil)
		r.AddCookie(&http.Cookie{Name: g.sessionCookieName(), Value: token})
		r.Header.Set("Origin", "http://127.0.0.1:4777") // forged, and free to forge
		return r
	}
	for _, target := range []string{"/api/messages", "/api/admin/role", "/api/me", "/api/act/announce"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if g.godViewAuthorized(thief(method, target)) {
				t.Errorf("%s %s was allowed on a replayed cookie alone: mail, identity, "+
					"acting as the human and role granting all sit behind this",
					method, target)
			}
		}
	}
	// Round five demanded an Origin header and called it fixed. This is the
	// request that defeated it, spelled out so the premise cannot come back:
	// forging Origin is free for anything that is not a browser.
	if g.godViewAuthorized(thief(http.MethodPost, "/api/admin/role")) {
		t.Error("a forged same-origin write reached role granting")
	}

	// The BOARD: same cookie, plus the key it took from the fragment.
	page := func(method, target string) *http.Request {
		r := thief(method, target)
		r.Header.Set(pageKeyHeader, pageKey)
		return r
	}
	for _, target := range []string{"/api/messages", "/api/admin/role", "/api/me"} {
		if !g.godViewAuthorized(page(http.MethodPost, target)) {
			t.Errorf("the board's own page was refused %s, so this refuses everybody, "+
				"which is a broken board rather than a closed hole", target)
		}
	}
	// A WRONG key is not a missing one, and must not pass either.
	wrong := thief(http.MethodPost, "/api/admin/role")
	wrong.Header.Set(pageKeyHeader, "not-the-key")
	if g.godViewAuthorized(wrong) {
		t.Error("any non-empty page key was accepted")
	}

	// The two routes the cookie still opens: the document, and the stream the
	// page must open with EventSource, which cannot send a header. Neither
	// carries mail. This is the residual, and it is deliberate: see SECURITY.md.
	for _, target := range []string{"/", "/events"} {
		if !g.godViewAuthorized(thief(http.MethodGet, target)) {
			t.Errorf("GET %s was refused, so the board cannot load itself", target)
		}
	}
	// But a hostile PAGE still cannot write through them: a browser sets Origin
	// itself and will not let a page lie about it.
	crossOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4777/", nil)
	crossOrigin.AddCookie(&http.Cookie{Name: g.sessionCookieName(), Value: token})
	crossOrigin.Header.Set("Origin", "http://127.0.0.1:9999")
	if g.godViewAuthorized(crossOrigin) {
		t.Error("a write from another port's origin was accepted")
	}
}

// A logged-in browser must still be able to load the board's own links.
//
// The page key closed the coordination tier to a bare cookie, which was right:
// /mcp lives in that tier and register hands out an agent token. But a browser
// cannot put a custom header on an ordinary navigation or on a favicon request,
// so the two things the board's own HTML points at, the icon in its <link> and
// the Protocol anchor a reader clicks, started returning 401 to a perfectly
// valid session. The operator sees a broken icon and a dead link and concludes
// the board is broken. Found by the pre-release review.
//
// Both are safe: /help renders a static template with nil data and /icon.svg is
// an image. Neither carries board state, and the protocol documentation is
// published anyway.
func TestABoardSessionCanLoadTheIconAndTheProtocolLink(t *testing.T) {
	g := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
	const token, pageKey = "a-real-session", "a-real-page-key"
	gate := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	g.sessions[token] = boardSession{exp: time.Now().Add(time.Hour), page: pageKey}

	browse := func(method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:4777"+target, nil)
		r.AddCookie(&http.Cookie{Name: g.sessionCookieName(), Value: token})
		// No page key: a browser cannot add one to a navigation or a favicon.
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, r)
		return rec
	}

	for _, target := range []string{"/icon.svg", "/help"} {
		if got := browse(http.MethodGet, target).Code; got == http.StatusUnauthorized {
			t.Errorf("GET %s returned 401 for a valid board session: the browser cannot "+
				"send the page key on this request, so the board's own icon and "+
				"Protocol link break for a logged-in operator", target)
		}
	}

	// And the allowance stays a CLOSED list. The reason the page key exists is
	// that this same tier holds /mcp, where register hands out an agent token
	// to anything that asks; a rule wide enough to be convenient here is how
	// that got exposed the first time.
	for _, target := range []string{"/mcp", "/api/messages", "/api/admin/role"} {
		if got := browse(http.MethodGet, target).Code; got != http.StatusUnauthorized {
			t.Errorf("GET %s returned %d for a cookie with no page key: the browsable "+
				"exception has widened past the two routes a browser actually needs",
				target, got)
		}
	}

	// A write to a browsable route is still refused: the exception is for
	// fetching a page, not for doing anything.
	if got := browse(http.MethodPost, "/help").Code; got != http.StatusUnauthorized {
		t.Errorf("POST /help returned %d: the exception must not carry state changes", got)
	}
}

// Two boards on one host do not overwrite each other's browser session.
//
// Cookies are host-scoped and never port-scoped, so both boards wrote
// `dibs_session` and whichever was redeemed last won. `-allow-parallel` exists
// so an operator can run separate boards for agents they do not trust together,
// and their web interfaces could not both stay signed in: the older tab kept
// its own port-scoped page key and began sending the other board's session
// token, so stream revalidation and every keyed request failed with nothing on
// screen to explain it. The boards stayed isolated; their operator could not
// use them. Every existing test builds one daemon and one cookie jar.
func TestTwoBoardsOnOneHostDoNotShareACookieName(t *testing.T) {
	a := newAuthGate("secret-a", "", "127.0.0.1:4777")
	b := newAuthGate("secret-b", "", "127.0.0.1:4778")

	if a.sessionCookieName() == b.sessionCookieName() {
		t.Fatalf("both boards issue %q. A cookie is host-scoped, so redeeming the "+
			"second board's link overwrites the first board's session and signs the "+
			"operator out of a board they never touched",
			a.sessionCookieName())
	}

	// The name has to identify the board, not merely differ: a random one would
	// pass the check above and be a new cookie on every restart.
	for _, c := range []struct {
		g    *authGate
		port string
	}{{a, "4777"}, {b, "4778"}} {
		if !strings.Contains(c.g.sessionCookieName(), c.port) {
			t.Errorf("the cookie for the board on :%s is %q, which does not name it. "+
				"It must be derived from the port, so it is stable across restarts "+
				"and distinct from every other board on this host",
				c.port, c.g.sessionCookieName())
		}
	}

	// A board whose listen address could not be parsed still gets a workable
	// name rather than an empty one.
	if n := newAuthGate("s", "", "not-an-address").sessionCookieName(); n == "" {
		t.Error("a daemon with an unparseable listen address issues a nameless cookie")
	}
}
