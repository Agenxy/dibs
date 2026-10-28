// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/transfer"
	"golang.org/x/crypto/acme/autocert"
)

type publicConfig struct {
	URL, Addr, Host, Mode string
	IP                    netip.Addr
	TLS                   *tls.Config
	Endpoint              *invites.PublicEndpoint
	guestPoll             time.Duration // zero in production; bounded-clock listener fixtures shorten the timer
}

func resolvePublic(o *daemonOpts) (publicConfig, error) {
	resolved, err := boardconfig.ResolvePublic(publicOptions(o))
	if err != nil {
		return publicConfig{}, err
	}
	if resolved.IP.IsValid() {
		if err := assignedGuestIP(resolved.IP); err != nil {
			return publicConfig{}, err
		}
	}
	return publicConfig{
		URL: resolved.URL, Addr: resolved.Addr, Host: resolved.Host, IP: resolved.IP, Mode: resolved.Mode,
	}, nil
}

func globalGuestIPv6(ip netip.Addr) bool { return boardconfig.IsPublicGuestIPv6(ip) }

func assignedGuestIP(ip netip.Addr) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("public-ip assignment could not be measured: %w; retry after checking the interface", err)
	}
	if len(interfaces) > 64 {
		return errors.New("public-ip assignment snapshot exceeds 64 interfaces; " +
			"reduce the host interface set before advertising a guest endpoint")
	}
	count := 0
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		count += len(addresses)
		if count > 256 {
			return errors.New("public-ip assignment snapshot exceeds 256 addresses; " +
				"reduce the host address set before advertising a guest endpoint")
		}
		for _, addr := range addresses {
			prefix, err := netip.ParsePrefix(addr.String())
			if err == nil && prefix.Addr() == ip {
				return nil
			}
		}
	}
	return errors.New("public-ip is not assigned to an up interface; " +
		"choose an address actually assigned on this host (stability remains operator-asserted)")
}

func preparePublic(c *publicConfig, dir string) error {
	if !c.IP.IsValid() {
		return nil
	}
	config, caPEM, pin, err := guestTLS(dir, c.IP)
	if err != nil {
		return err
	}
	c.TLS = config
	c.Endpoint = invites.NewPublicEndpoint(invites.EndpointInfo{
		URL: c.URL, Mode: "direct-ip", Address: c.IP.String(), Stability: "operator-asserted",
		CAPEM: caPEM, CASPKIPin: pin, VerifiedClients: []string{},
	})
	return nil
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
			publicError(w, http.StatusForbidden, "E_ORIGIN", "forbidden origin", "use the configured public origin")
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
	if c.IP.IsValid() && (c.Endpoint == nil || c.TLS == nil) {
		if err := preparePublic(&c, dir); err != nil {
			return nil, nil, err
		}
	}
	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("public listener: %w; choose an unused public-addr", err)
	}
	directTLS := c.TLS
	s := mcp.New(eng)
	s.SetTaskKey(secret)
	publicFiles := files
	if c.Mode == "loopback-forward" {
		publicFiles = nil
	}
	s.SetTransfers(publicFiles, c.URL)
	g := &publicGate{
		store: invites.Store{Dir: dir}, origin: c.URL, rates: map[string]*inviteBucket{},
		transfers: publicFiles, proxy: c.Host == "" && !c.IP.IsValid(),
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
	} else if directTLS != nil {
		srv.TLSConfig = directTLS
	}
	closeFn := func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		_ = ln.Close()
	}
	if c.Endpoint != nil && directTLS != nil {
		startGuestMonitor(ctx, c, dir, srv, ln)
	}
	go func() { <-ctx.Done(); closeFn() }()
	go servePublic(srv, ln, c.Endpoint, failure, stop)
	slog.Info("public invitation listener up", "mcp", c.URL+"/mcp", "addr", c.Addr,
		"mode", "invite-only, pull-only; private routes remain on the original listener")
	return failure, closeFn, nil
}

func servePublic(srv *http.Server, ln net.Listener, endpoint *invites.PublicEndpoint,
	failure chan<- error, stop context.CancelFunc,
) {
	var err error
	if srv.TLSConfig != nil {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	if endpoint != nil {
		endpoint.Withdraw("guest listener stopped; restart after fixing the selected address")
		slog.Warn("guest listener stopped; private board remains available", "error", err)
		return
	}
	failure <- fmt.Errorf("public listener stopped: %w", err)
	stop()
}

func startGuestMonitor(ctx context.Context, c publicConfig, dir string, srv *http.Server, ln net.Listener) {
	// Atomic snapshots keep leaf renewal out of handshakes and races. Never
	// switch a guest CA under a pinned client. Loss/expiry withdraws only the
	// guest endpoint; the private fleet board remains available.
	current := new(atomic.Pointer[tls.Config])
	current.Store(c.TLS)
	srv.TLSConfig = c.TLS.Clone()
	srv.TLSConfig.GetConfigForClient = func(_ *tls.ClientHelloInfo) (*tls.Config, error) { return current.Load(), nil }
	withdraw := func(err error) {
		c.Endpoint.Withdraw(err.Error())
		slog.Warn("public guest endpoint withdrawn", "reason", err.Error(), "hint",
			"fix the assigned public-ip; changed scope requires fresh guest CA trust and invitations, not fleet CA rotation")
		closePublicConnections(srv, ln)
	}
	go monitorGuest(ctx, c, dir, current, withdraw)
}

func closePublicConnections(srv *http.Server, ln net.Listener) {
	// Shutdown lets existing long polls linger; a withdrawn address must not
	// keep an invitation channel alive through an already-open connection.
	_ = srv.Close()
	_ = ln.Close()
}

func monitorGuest(ctx context.Context, c publicConfig, dir string,
	current *atomic.Pointer[tls.Config], withdraw func(error),
) {
	interval := c.guestPoll
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := assignedGuestIP(c.IP); err != nil {
				withdraw(err)
				return
			}
			leaf := current.Load().Certificates[0].Leaf
			if leaf.NotAfter.After(time.Now().Add(time.Hour)) {
				continue
			}
			renewed, _, pin, err := guestTLS(dir, c.IP)
			if err != nil {
				withdraw(err)
				return
			}
			if pin != c.Endpoint.Snapshot().CASPKIPin {
				withdraw(errors.New("guest CA changed; re-invite with newly verified trust material, not a silent trust update"))
				return
			}
			current.Store(renewed)
		}
	}
}
