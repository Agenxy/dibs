package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
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
//   - GOD-VIEW (decrypted mail, web board): a session minted ONLY by proving the
//     human ADMIN PASSWORD. The file secret is NOT sufficient, so an agent that
//     read the secret file still cannot curl /api/messages. Presence upgrade
//     (TouchID) can later replace the password on capable platforms.
type authGate struct {
	secret        string
	adminHashPath string
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

func newAuthGate(secret, adminHashPath string) *authGate {
	return &authGate{
		secret: secret, adminHashPath: adminHashPath,
		boot: map[string]time.Time{}, sessions: map[string]time.Time{},
	}
}

// adminHash reads the admin verifier fresh each time, so `dibs admin
// set-password` takes effect without a daemon restart. "" = not set.
func (g *authGate) adminHash() string {
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

// SessionStillValid re-checks a request's session mid-flight.
//
// The gate runs once, when a request enters it. An SSE stream is ONE long-lived
// request, so a god-view connection opened a second before expiry kept
// delivering decrypted mail indefinitely. Verified with a shortened TTL: a new
// request with the same cookie got 401 while the already-open stream carried a
// message sent after the deadline.
//
// Exported so the streaming handler can ask again as it goes; a deadline that
// is only checked at the door is not a deadline.
func (g *authGate) SessionStillValid(r *http.Request) bool { return g.validSession(r) }

func (g *authGate) validSession(r *http.Request) bool {
	c, err := r.Cookie("lanes_session")
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
		return true
	}
	if h := g.adminHash(); h != "" && g.headerSecret(r) && adminpw.Verify(r.Header.Get("X-Dibs-Admin"), h) {
		return true
	}
	return false
}

func (g *authGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !localOrigin(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		// Mint a god-view bootstrap token: requires BOTH the local secret
		// (same-user) AND the admin password (human). `dibs web` calls this.
		if r.Method == http.MethodPost && r.URL.Path == "/bootstrap" {
			if !g.headerSecret(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			hash := g.adminHash()
			if hash == "" {
				http.Error(w, "no admin password set: run `dibs admin set-password` to enable the board", http.StatusForbidden)
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
					Name: "lanes_session", Value: sess, Path: "/",
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

func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
