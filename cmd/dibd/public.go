package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/transfer"
	xport "github.com/agenxy/dibs/internal/transport"
	"golang.org/x/crypto/acme/autocert"
)

type publicConfig struct{ URL, Addr, Host string }

func dnsHostname(host string) bool {
	if len(host) > 253 || host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range strings.ToLower(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func resolvePublic(o *daemonOpts) (publicConfig, error) {
	var c publicConfig
	if *o.publicHost != "" && *o.publicURL != "" {
		return c, errors.New("choose public-host OR public-url, not both")
	}
	if *o.publicHost == "" && *o.publicURL == "" {
		if *o.publicAddr != "" || *o.acceptACME {
			return c, errors.New("public-addr and acme-accept-terms require a public-host or public-url")
		}
		return c, nil
	}
	if host := strings.ToLower(*o.publicHost); host != "" {
		return resolveACMEPublic(o, host)
	}
	return resolveProxyPublic(o)
}

func resolveACMEPublic(o *daemonOpts, host string) (publicConfig, error) {
	var c publicConfig
	if !dnsHostname(host) || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return c, errors.New("public-host must be a DNS hostname, not a URL, IP or wildcard")
	}
	if !*o.acceptACME {
		return c, errors.New("public-host needs explicit --acme-accept-terms; " +
			"alternatively use --public-url behind your own TLS proxy")
	}
	c = publicConfig{URL: "https://" + host, Addr: firstNonEmpty(*o.publicAddr, ":443"), Host: host}
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil || port != "443" {
		return c, errors.New("public-host uses TLS-ALPN-01: public-addr must listen on port 443; " +
			"use public-url for a TLS-terminating proxy")
	}
	return c, nil
}

func resolveProxyPublic(o *daemonOpts) (publicConfig, error) {
	var c publicConfig
	u, err := url.Parse(*o.publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("public-url must be an HTTPS origin without credentials, query, fragment or path; " +
			"e.g. https://board.example.com")
	}
	if !dnsHostname(u.Hostname()) && net.ParseIP(u.Hostname()) == nil {
		return c, errors.New("public-url requires a DNS hostname or IP literal")
	}
	u.Host = strings.ToLower(u.Host)
	c = publicConfig{URL: strings.TrimSuffix(u.String(), "/"), Addr: firstNonEmpty(*o.publicAddr, "127.0.0.1:4778")}
	if !xport.IsLoopback(c.Addr) {
		return c, errors.New("public-url's plaintext proxy listener must be loopback; set --public-addr 127.0.0.1:4778")
	}
	return c, nil
}

type publicGate struct {
	transfers *transfer.Manager
	proxy     bool
	store     invites.Store
	service   *invites.Service
	origin    string
	mu        sync.Mutex
	rates     map[string]*inviteBucket
}

type inviteBucket struct {
	tokens float64
	at     time.Time
}

func (g *publicGate) allow(name string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.rates[name]
	if b == nil {
		if len(g.rates) >= 2048 {
			for id, stale := range g.rates {
				if now.Sub(stale.at) > time.Minute {
					delete(g.rates, id)
				}
			}
			if len(g.rates) >= 2048 {
				return false
			}
		}
		b = &inviteBucket{tokens: 30, at: now}
		g.rates[name] = b
	}
	b.tokens = min(30, b.tokens+now.Sub(b.at).Seconds()*10)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func publicError(w http.ResponseWriter, status int, code, message, hint string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message, "hint": hint})
}

func (g *publicGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if g.transfers != nil && strings.HasPrefix(r.URL.Path, transfer.Prefix) {
			g.transfers.Handler(g.origin, g.proxy).ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/mcp" || r.URL.RawQuery != "" || r.Method != http.MethodPost {
			publicError(w, http.StatusForbidden, "E_INVITE_SCOPE", "the public listener opens only POST /mcp",
				"use /mcp for agent coordination; local/human routes are not published here")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != g.origin {
			publicError(w, http.StatusForbidden, "E_ORIGIN", "forbidden origin", "use the configured public HTTPS origin")
			return
		}
		auth := strings.Fields(r.Header.Get("Authorization"))
		if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || r.Header.Get("X-Dibs-Local") != "" {
			publicError(w, http.StatusUnauthorized, "E_INVITE_AUTH", "an invitation bearer credential is required",
				"use Authorization: Bearer with the key printed by dibs invite; never the board secret")
			return
		}
		e, err := g.store.Authenticate(auth[1], time.Now())
		if err != nil {
			publicError(w, http.StatusUnauthorized, "E_INVITE_AUTH", "invitation refused",
				"ask the operator for a live invitation; expired, revoked and unknown credentials are not accepted")
			return
		}
		if !g.allow(e.Name, time.Now()) {
			w.Header().Set("Retry-After", "1")
			publicError(w, http.StatusTooManyRequests, "E_RATE_LIMITED", "invite rate limit exceeded",
				"back off briefly and retry")
			return
		}
		live, err := g.service.Live(r.Context(), e)
		if err != nil || !live {
			publicError(w, http.StatusUnauthorized, "E_INVITE_AUTH", "invitation issuer closed or disappeared",
				"ask a current issuer for a new invitation; reopening its old identity never revives closed children")
			return
		}
		ctx := engine.WithInvitation(r.Context(), engine.Invitation{
			Name: e.Name, AgentID: e.AgentID,
			IssuedBy: e.IssuedBy, IssuerCreated: e.IssuerCreated, IssuerClosed: e.IssuerClosed,
		})
		ctx = mcp.WithInviteBinding(ctx, func(id string) error { return g.store.Bind(e, id) })
		ctx = transfer.WithInvitationEntry(ctx, e)
		r = r.Clone(ctx)
		// Invitation is the door, not the agent token. Never let the normal
		// MCP bearer fallback reinterpret it as an agent recovery credential.
		r.Header.Del("Authorization")
		r.Header.Del("X-Dibs-Agent-Nonce")
		next.ServeHTTP(w, r)
	})
}

// startPublic binds a second listener; no private mux is ever handed to it.
// The private/local/pinned endpoint and its credential remain unchanged.
func startPublic(ctx context.Context, c publicConfig, dir string, eng *engine.Engine,
	secret string, stop context.CancelFunc, files *transfer.Manager,
) (<-chan error, func(), error) {
	failure := make(chan error, 1)
	if c.URL == "" {
		return failure, func() {}, nil
	}
	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("public listener: %w; choose an unused public-addr", err)
	}
	s := mcp.New(eng)
	s.SetTaskKey(secret)
	s.SetTransfers(files, c.URL)
	g := &publicGate{
		store: invites.Store{Dir: dir}, origin: c.URL, rates: map[string]*inviteBucket{},
		transfers: files, proxy: c.Host == "",
	}
	g.service = &invites.Service{Store: g.store, Engine: eng}
	srv := &http.Server{
		Addr: c.Addr, Handler: g.wrap(s), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	if c.Host != "" {
		manager := &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			Cache:  autocert.DirCache(filepath.Join(dir, "acme-cache")), HostPolicy: autocert.HostWhitelist(c.Host),
		}
		srv.TLSConfig = manager.TLSConfig()
		srv.TLSConfig.MinVersion = tls.VersionTLS13
	}
	closeFn := func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		_ = ln.Close()
	}
	go func() { <-ctx.Done(); closeFn() }()
	go func() {
		var err error
		if c.Host != "" {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			failure <- fmt.Errorf("public listener stopped: %w", err)
			stop()
		}
	}()
	slog.Info("public invitation listener up", "mcp", c.URL+"/mcp", "addr", c.Addr,
		"mode", "invite-only, pull-only; private routes remain on the original listener")
	return failure, closeFn, nil
}
