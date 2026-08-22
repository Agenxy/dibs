package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/humanauth"
	"github.com/agenxy/dibs/internal/web"
)

// loadOrCreateSecret returns the local access secret (SPEC §5): loopback TCP is
// reachable by other OS users, so every request must present this 0600-file
// value. The secret gates the COORDINATION surface (agents, MCP). It travels
// only in the X-Dibs-Local / Authorization header: never a URL or cookie.
func loadOrCreateSecret(path string) (string, error) {
	// #nosec G304 -- a path inside the daemon's own data directory, or one the
	// operator pointed the CLI at. Same-user access only; refusing it would mean
	// refusing to run.
	b, err := os.ReadFile(path)
	if err == nil && len(b) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	s := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		return "", err
	}
	return s, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func strconvItoa(n int) string { return strconv.Itoa(n) }

// authGate enforces two trust tiers:
//   - COORDINATION (agents, MCP tools, public board): the local secret via
//     header. A same-user agent legitimately holds this.
//   - GOD-VIEW (decrypted mail, web board): a session minted only by proving a
//     HUMAN is here, by Touch ID where the machine can do it and by the admin
//     PASSWORD where it cannot. The file secret is NOT sufficient on its own, so
//     an agent that read the secret file still cannot curl /api/messages.
//
// Presence is preferred, and that is not a convenience. A password proves
// possession of a secret an agent could in principle have been handed; a
// fingerprint proves somebody is sitting there. internal/mcp/human.go has said
// exactly that about the panel's unlock since it was written, and this gate
// went on demanding the weaker of the two: a person with a working sensor had
// to invent, store and type a password in order to be trusted less.
//
// The check runs HERE, in the daemon, never in the client. A caller that merely
// asserted "I verified presence" would be forgeable by anything holding the
// local secret, which is every agent on the machine: the whole point is that the
// proof cannot be produced from inside the transport.
//
// The password stays, because presence genuinely is not always available: no
// sensor, Linux, a headless session. Both are offered and the caller is told
// which one this machine can do, rather than being asked for a finger it has no
// way to read.
type authGate struct {
	secret        string
	adminHashPath string
	// host and port are the daemon's own listening address, so an Origin can be
	// checked against THIS server rather than against a fixed idea of what a
	// server's address looks like. See localOrigin.
	host, port    string
	mu            sync.Mutex
	boot          map[string]time.Time // one-time bootstrap tokens
	sessions      map[string]time.Time // god-view session cookie tokens
	adminFails    int                  // consecutive wrong admin passwords
	adminLockTill time.Time            // throttle window for /bootstrap
}

const (
	bootstrapTTL = 2 * time.Minute
	sessionTTL   = 12 * time.Hour
)

func newAuthGate(secret, adminHashPath, listenAddr string) *authGate {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		host, port = "", ""
	}
	return &authGate{
		host: host, port: port,
		secret: secret, adminHashPath: adminHashPath,
		boot: map[string]time.Time{}, sessions: map[string]time.Time{},
	}
}

// adminHash reads the admin verifier fresh each time, so `dibs admin
// set-password` takes effect without a daemon restart. "" = not set.
func (g *authGate) adminHash() string {
	// #nosec G703 -- adminHashPath is filepath.Join(*dir, "admin.hash"), where
	// -dir is the operator's own flag or DIBS_DIR. It has never been caller
	// input. The taint analysis began reaching it when newAuthGate gained the
	// listen address, which arrives from the same flags: two operator-supplied
	// values in one constructor is enough for the pass to connect them, and the
	// connection is not one an agent can reach.
	b, err := os.ReadFile(g.adminHashPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (g *authGate) mintBootstrap() string {
	t := randHex(32)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gcLocked()
	g.boot[t] = time.Now().Add(bootstrapTTL)
	return t
}

func (g *authGate) redeemBootstrap(t string) (string, bool) {
	if t == "" {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.boot[t]
	delete(g.boot, t) // single use
	if !ok || time.Now().After(exp) {
		return "", false
	}
	sess := randHex(32)
	g.sessions[sess] = time.Now().Add(sessionTTL)
	return sess, true
}

func (g *authGate) validSession(r *http.Request) bool {
	c, err := r.Cookie("dibs_session")
	if err != nil || c.Value == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(g.sessions, c.Value)
		return false
	}
	return true
}

func (g *authGate) gcLocked() {
	now := time.Now()
	for k, exp := range g.boot {
		if now.After(exp) {
			delete(g.boot, k)
		}
	}
	for k, exp := range g.sessions {
		if now.After(exp) {
			delete(g.sessions, k)
		}
	}
}

func (g *authGate) headerSecret(r *http.Request) bool {
	if h := r.Header.Get("X-Dibs-Local"); h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(g.secret)) == 1 {
		return true
	}
	// The auth scheme is case-INSENSITIVE (RFC 9110 §11.1), so "bearer" and
	// "BEARER" are the same credential as "Bearer". Matching only the
	// capitalised spelling rejected conforming clients as unauthenticated, which
	// presents as "my token does not work" with nothing in the log to explain
	// it. The TOKEN comparison stays constant-time; only the scheme is folded.
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if found && strings.EqualFold(scheme, "Bearer") &&
		subtle.ConstantTimeCompare([]byte(token), []byte(g.secret)) == 1 {
		return true
	}
	return false
}

// godViewPath reports whether a path exposes decrypted mail (the human
// god-view). It normalizes first so slash/dot-segment variants
// (/api//messages, /api/messages/, /x/../api/messages) can't dodge the gate
// and then reach the same handler via the mux's own path cleaning.
func godViewPath(p string) bool {
	clean := path.Clean("/" + p)
	// /api/admin/ is gated by PREFIX, not by naming the two routes that exist
	// today. Enumerating them worked, but it fails OPEN: the next admin route
	// somebody adds is ungated until they remember to come back here, and the
	// failure is silent: the route works, for everyone. SECURITY.md documents
	// the prefix; this is the code that makes that true rather than lucky.
	// Both the subtree AND its root. path.Clean("/api/admin/") is "/api/admin",
	// which has no trailing slash and so fails the prefix test: a handler
	// registered at "/api/admin", "/api/admin/" or "/api/admin/{$}" would have
	// failed OPEN, which is the same fail-open shape that enumerating the two
	// known routes had. Fixing one and leaving the other is not fixing it.
	if clean == "/api/admin" || strings.HasPrefix(clean, "/api/admin/") {
		return true
	}
	switch clean {
	case "/", "/events", "/api/messages":
		return true
	// The human's IDENTITY and everything that ACTS as them.
	//
	// These were missing, and the consequence was privilege escalation rather
	// than mere visibility: /api/act/* falls through to the coordination tier,
	// which accepts the local secret alone, and every agent must hold that
	// secret to call /mcp at all. Verified against a running daemon: an
	// ordinary agent POSTed /api/act/join and /api/act/announce with nothing
	// but the coordination secret and got 200 both times, and /api/me returned
	// the operator's agent id.
	//
	// The announcement path is the worst of them: it creates an obligation on
	// every other member of an agent, attributed to a human who never said it.
	// "An agent holding the secret still lacks the password, so it cannot pass"
	// is the promise two comments above this one; it was not true of these
	// routes.
	case "/api/me":
		return true
	}
	// Prefix, because /api/act/<what> is a family. Compared after cleaning so
	// /api//act/post and /x/../api/act/post cannot dodge the gate and still
	// reach the handler through the mux's own cleaning.
	return strings.HasPrefix(clean, "/api/act/")
}

// godViewAuthorized: a browser session cookie (from the magic-link), OR: for
// the CLI: the local secret PLUS the admin password in headers. Both require
// the admin password somewhere; the file secret alone is never enough. An agent
// holding the secret still lacks the password, so it cannot pass.
func (g *authGate) godViewAuthorized(r *http.Request) bool {
	if g.validSession(r) {
		// A cookie authenticates a BROWSER, so a state-changing request carrying
		// one has to look like it came from a browser on this board.
		//
		// Cookies are host-scoped and not port-scoped, and every port on one
		// loopback hostname is the same site, so SameSite=Strict does not
		// separate them either. A second server on 127.0.0.1 therefore receives
		// dibs_session the moment the operator visits it, and can then replay it
		// from outside a browser, where no Origin header is sent at all and the
		// check in wrap() is skipped by construction. That reached
		// /api/admin/role, which grants roles. Demonstrated by the pre-release
		// review with a cookie jar and a handler-level replay.
		//
		// Browsers send Origin on POST and friends, and a non-browser replay does
		// not, so requiring it here costs the board nothing and costs the replay
		// everything. Reads are deliberately not gated this way: a same-origin
		// GET does not carry Origin either, and refusing those would refuse the
		// board itself. See SECURITY.md for what that leaves.
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return true
		}
		o := r.Header.Get("Origin")
		if o == "" || !g.localOrigin(o, r.Host) {
			return false
		}
		return true
	}
	if h := g.adminHash(); h != "" && g.headerSecret(r) && adminpw.Verify(r.Header.Get("X-Dibs-Admin"), h) {
		return true
	}
	return false
}

// presenceBootstrap mints a god-view session against a fingerprint instead of a
// password.
//
// The local secret has already been checked by the caller, so this is the
// second factor and not the only one: same-user AND a person who consented just
// now. An agent that tried it would raise the system sheet on the operator's own
// Mac, with the reason below written on it, and the operator would decline.
//
// The three verdicts get three different answers because the remedies are
// opposite, and collapsing them is how a person ends up retyping a password on a
// machine that was willing to take their finger, or waiting for a sheet that
// this machine can never show.
func (g *authGate) presenceBootstrap(w http.ResponseWriter, r *http.Request) {
	// Written on the system sheet, so the person is approving a thing rather
	// than approving that something is happening.
	//
	// AND SO AN UNEXPECTED ONE IS REFUSABLE. This read "open the Dibs board,
	// which shows every agent's decrypted mail", and the argument for safety
	// was a comment saying an agent that tried it would raise the sheet and the
	// operator would decline. That rests on the operator noticing, and the
	// sentence gave them nothing to notice: it describes exactly what they
	// would be doing if they had just run `dibs web` themselves, so a prompt
	// they did not cause is indistinguishable from one they did. An agent
	// holding the local secret can raise this at any moment, including a moment
	// when the operator is opening the board anyway.
	//
	// It cannot name the requester the way internal/mcp/human.go now does,
	// because this path authenticates with the local secret and no agent
	// identity reaches it. What it can do is say that the credential goes back
	// to whoever asked, which is the fact that makes an unprompted sheet worth
	// declining. A pre-release review reproduced the escalation: a request
	// received the token, redeemed it, and reached an admin role handler.
	const reason = "give a full-access board session to whatever just asked for it: " +
		"every agent's decrypted mail, and the power to change roles. Decline this " +
		"unless you started it yourself, just now"
	verdict, err := humanauth.Check(r.Context(), reason)
	if err != nil && verdict != humanauth.Unavailable {
		http.Error(w, "presence check failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	switch verdict {
	case humanauth.Verified:
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		out := map[string]string{"bt": g.mintBootstrap(), "proof": "presence"}
		// A dev build answering from a script says so, in the response, every
		// time: the same rule internal/mcp/human.go holds. A mocked unlock that
		// looked identical to a real one would be evidence of nothing, in exactly
		// the artefact somebody would later cite as evidence it works.
		if humanauth.Mocked() {
			out["mocked"] = "NO HUMAN WAS CHECKED: scripted verdict from a dev build"
		}
		_ = json.NewEncoder(w).Encode(out)
	case humanauth.Unavailable:
		// Not a refusal, and it must not read as one. There is nobody to blame
		// and nothing to retry; the answer is the other proof.
		http.Error(w, "this machine cannot check presence: use the admin password "+
			"(`dibs admin set-password`, then `dibs web`)", http.StatusPreconditionFailed)
	case humanauth.Abandoned:
		http.Error(w, "the presence check went away before anybody answered: nothing "+
			"was unlocked", http.StatusRequestTimeout)
	default: // Declined
		http.Error(w, "presence was declined: nothing was unlocked", http.StatusUnauthorized)
	}
}

func (g *authGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !g.localOrigin(o, r.Host) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		// The board may not be put in somebody else's frame.
		//
		// The origin check above stops a hostile page CALLING this daemon. It
		// does nothing about a hostile page EMBEDDING it, and the difference
		// matters here more than on most sites: cookies are host-scoped rather
		// than port-scoped, and different ports on one loopback hostname are
		// same-site, so a page on http://localhost:9999 can frame the
		// authenticated board on :4777 with its twelve-hour session attached.
		// Anything it then induces a click on is issued by the board's OWN
		// script, so the Origin is this daemon's and the check above waves it
		// through. Overlay something plausible and the human approves a grant or
		// an adoption they never read. Any local process can bind a port, which
		// here includes the agents this daemon coordinates.
		//
		// Both headers, because the modern one is authoritative where it is
		// understood and the old one is what everything else obeys. Found by a
		// pre-release review.
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")

		// Mint a god-view bootstrap token: requires BOTH the local secret
		// (same-user) AND the admin password (human). `dibs web` calls this.
		if r.Method == http.MethodPost && r.URL.Path == "/bootstrap" {
			if !g.headerSecret(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Presence first, when the caller asked for it. This blocks on a
			// person for up to humanauth's own timeout, which is fine on an HTTP
			// handler goroutine and would not be on the engine's single writer:
			// nothing here touches it.
			if r.Header.Get("X-Dibs-Presence") == "1" {
				g.presenceBootstrap(w, r)
				return
			}
			hash := g.adminHash()
			if hash == "" {
				http.Error(w, "no admin password is set on this board: run "+
					"`dibs admin set-password`. (Touch ID opens it without one, but this "+
					"request did not ask for a presence check, or asked and did not get "+
					"a person.)", http.StatusForbidden)
				return
			}
			// Throttle wrong-password attempts: the admin password guards all
			// decrypted mail, so bound online brute force. After 5 misses,
			// lock with exponential backoff (5s doubling, capped at 5m).
			g.mu.Lock()
			if wait := time.Until(g.adminLockTill); wait > 0 {
				g.mu.Unlock()
				w.Header().Set("Retry-After", strconvItoa(int(wait.Seconds())+1))
				http.Error(w, "too many attempts: wait "+wait.Round(time.Second).String(), http.StatusTooManyRequests)
				return
			}
			g.mu.Unlock()
			if !adminpw.Verify(r.Header.Get("X-Dibs-Admin"), hash) {
				g.mu.Lock()
				g.adminFails++
				if g.adminFails >= 5 {
					back := 5 * time.Second << min(g.adminFails-5, 6) // 5s..~5m
					if back > 5*time.Minute {
						back = 5 * time.Minute
					}
					g.adminLockTill = time.Now().Add(back)
				}
				g.mu.Unlock()
				http.Error(w, "wrong admin password", http.StatusUnauthorized)
				return
			}
			g.mu.Lock()
			g.adminFails, g.adminLockTill = 0, time.Time{}
			g.mu.Unlock()
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"bt": g.mintBootstrap()})
			return
		}

		// Redeem: /?bt=<one-time> → god-view session cookie, redirect clean.
		if bt := r.URL.Query().Get("bt"); bt != "" {
			if sess, ok := g.redeemBootstrap(bt); ok {
				// #nosec G124 -- Secure is set from r.TLS on the line below: a Secure cookie
				// over plain HTTP is never sent back, which would break the loopback board
				// rather than protect it. HttpOnly and SameSite=Strict are unconditional.
				http.SetCookie(w, &http.Cookie{
					Name: "dibs_session", Value: sess, Path: "/",
					HttpOnly: true, SameSite: http.SameSiteStrictMode,
					// Secure only when the connection actually is: the daemon
					// serves plain HTTP on loopback and TLS on any reachable
					// address, and a Secure cookie set over HTTP is simply never
					// sent back, which would silently break the local board.
					Secure: r.TLS != nil,
					MaxAge: int(sessionTTL.Seconds()),
				})
				// Redirect to the request's own path with the one-time token
				// stripped, but SANITISE it first.
				//
				// A path beginning "//" is protocol-relative: browsers read
				// "//evil.com/" as a different HOST, so echoing r.URL back would
				// turn this redemption endpoint into an open redirect that also
				// happens to hand out a fresh god-view session on the way past.
				// Anything not rooted at a single "/" goes to the board root.
				target := r.URL.EscapedPath()
				if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
					target = "/"
				}
				// #nosec G710 -- the target is sanitised immediately below (rooted single '/',
				// protocol-relative rejected) and covered by
				// TestBootstrapRedeemCannotRedirectOffHost. The taint analysis cannot see it.
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
		}

		// Liveness is open, and only liveness.
		//
		// Everything else here needs the coordination secret, which is right for
		// anything that reveals the board. "Is the process up" reveals nothing:
		// gating it meant the only way to supervise Dibs was to give a
		// monitoring system the same secret every agent authenticates with, and
		// an operator evaluating Dibs said so. The test for an open endpoint is
		// what it adds to what an attacker already has, and anyone who can reach
		// this port already learns the daemon is up by connecting to it.
		//
		// Cleaned, like godViewPath, so /livez/ and /x/../livez resolve here
		// rather than sliding past into the authenticated tier.
		if path.Clean("/"+r.URL.Path) == "/livez" {
			next.ServeHTTP(w, r)
			return
		}

		if godViewPath(r.URL.Path) {
			// Decrypted mail must never be cached to disk / bfcache, and must
			// never leak via Referer.
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if !g.godViewAuthorized(r) {
				g.unauthorized(w, r)
				return
			}
			// Long-lived handlers must be able to ask again: the gate runs once,
			// and an SSE stream outlives it by hours.
			r = r.WithContext(web.WithRevalidator(r.Context(), func() bool {
				return g.godViewAuthorized(r)
			}))
		} else if !g.headerSecret(r) && !g.validSession(r) {
			// Coordination tier: local secret (agents) OR a god-view session
			// (a logged-in human loading page assets). The file secret alone
			// can reach coordination but NOT the god-view paths above.
			g.unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *authGate) unauthorized(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(unauthorizedHTML))
		return
	}
	_, _ = w.Write([]byte("unauthorized: coordination needs the local secret (X-Dibs-Local); the board needs a " +
		"session from `dibs web` (admin password)\n"))
}

const unauthorizedHTML = `<!doctype html><meta charset=utf-8>
<title>Dibs: locked</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0b0e14;color:#e6ebf2;
font:15px/1.6 ui-sans-serif,-apple-system,system-ui,sans-serif}
@media(prefers-color-scheme:light){body{background:#f4f6f9;color:#1a2332}}
.card{max-width:460px;padding:32px 34px;text-align:center}
.mark{font-size:30px;color:#61d0a8;margin-bottom:10px}
h1{font-size:18px;margin:0 0 8px}p{color:#94a1b5;margin:8px 0}
code{font-family:ui-monospace,Menlo,monospace;font-size:13px;background:#151b26;border:1px solid #232c3d;
border-radius:7px;padding:9px 14px;display:inline-block;margin-top:6px;color:#e6ebf2}
@media(prefers-color-scheme:light){code{background:#fff;border-color:#dde4ee;color:#1a2332}}
</style>
<div class=card>
<div class=mark>▤</div>
<h1>This board shows private mail: it's locked</h1>
<p>Opening it takes your admin password (something an agent on this machine can't read). In your terminal:</p>
<code>dibs web</code>
<p>then open the link it prints. Single-use, expires in two minutes.</p>
</div>`

// localOrigin reports whether a browser Origin belongs to THIS daemon.
//
// THE CSRF THIS CLOSES. It checked the hostname alone and ignored the port, so
// every page served from any port on this machine passed. A cookie is scoped to
// a host and not to a port, and SameSite=Strict does not separate them either,
// so a hostile page on http://localhost:9999 could send a credentialed POST
// carrying the board session. It does not even need a preflight to do it:
// text/plain is CORS-safelisted, JSON travels inside it happily, and the
// handlers did not require a content type. CORS stops the attacker READING the
// answer and does nothing about the side effect, and the side effect here is
// GrantRole.
//
// Any local process can bind a port, which on this machine includes the agents
// this daemon coordinates. So the check is now against the origin of this
// server: the same port, over loopback. A page anywhere else is a different
// origin and is refused, which is what the browser's own model already says.
//
// Found by a pre-release review.
func (g *authGate) localOrigin(origin, reqHost string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// The daemon's OWN address, not a fixed idea of what one looks like.
	//
	// The first version of this check hardcoded the loopback hostnames, which
	// broke a configuration Dibs documents and supports: `addr` may be a LAN or
	// tailnet address so agents on other machines can reach the board, and such
	// a daemon then rejected its own board's origin. Navigation still loaded the
	// page, because ordinary navigation sends no Origin, so the board appeared
	// to work and every button on it returned 403. Found by a pre-release
	// review; it was a regression introduced by the fix for the port-blind
	// check, which is its own small lesson about tightening by pattern rather
	// than by identity.
	if g.port == "" {
		return isLoopback(u.Hostname()) // address unknown: the older, weaker rule
	}
	if u.Port() != g.port {
		return false
	}
	switch g.host {
	case "", "0.0.0.0", "::":
		// Listening on every interface, so the CONFIGURED host cannot narrow
		// anything. Compare against the host the browser actually addressed
		// instead of blessing every hostname on this port.
		//
		// It returned true here, and a same-site sibling is not a stranger to a
		// cookie: SameSite=Strict allows one, and text/plain is CORS-safelisted,
		// so a hostile page at another name under the same registrable domain
		// and port could POST to /api/admin/role with the session attached.
		// Narrow (it needs a wildcard bind, hostname access, and control of that
		// sibling) and free to close. Found by a pre-release review, which also
		// noted the test I wrote blessed the behaviour rather than checking it.
		return strings.EqualFold(u.Hostname(), hostOnly(reqHost))
	}
	if strings.EqualFold(u.Hostname(), g.host) {
		return true
	}
	// 127.0.0.1, ::1 and localhost are one server, and a browser may use any of
	// them for a daemon listening on any other.
	return isLoopback(u.Hostname()) && isLoopback(g.host)
}

func isLoopback(h string) bool {
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// hostOnly strips any port from a Host header.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
