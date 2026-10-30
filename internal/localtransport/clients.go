// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import (
	"context"
	"net"
	"net/http"
	"slices"
	"sync"
)

// Client is a currently connected HTTP client. Transport comes from the
// connection; PID/start and user-agent are labelled client reports. This is a
// derived, process-lifetime inventory, never coordination or ledger state.
type Client struct {
	ID            uint64 `json:"id"`
	Transport     string `json:"transport"`
	MCP           bool   `json:"mcp"`
	UserAgent     string `json:"user_agent,omitempty"`
	BridgePID     int    `json:"bridge_pid,omitempty"`
	BridgeStarted string `json:"bridge_started_at,omitempty"`
}

type (
	clientKey   struct{}
	observation struct {
		owner *Clients
		value Client
	}
)

// Clients mirrors the live connections already owned by net/http. Closed
// connections are removed, including idle connections closed by bridge re-exec.
type Clients struct {
	mu          sync.Mutex
	next        uint64
	connections map[net.Conn]*observation
}

// ConnContext verifies local peers and associates a derived live observation.
func (c *Clients) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	ctx = ConnContext(ctx, conn)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connections == nil {
		c.connections = make(map[net.Conn]*observation)
	}
	c.next++
	o := &observation{owner: c, value: Client{ID: c.next, Transport: "unobserved"}}
	c.connections[conn] = o
	return context.WithValue(ctx, clientKey{}, o)
}

// ConnState removes observations when their underlying connection ends.
func (c *Clients) ConnState(conn net.Conn, state http.ConnState) {
	if state != http.StateClosed && state != http.StateHijacked {
		return
	}
	c.mu.Lock()
	delete(c.connections, conn)
	c.mu.Unlock()
}

// Handler records transport on every private request, before dispatch. It
// never reads credential headers, query strings or bodies.
func (c *Clients) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o, ok := r.Context().Value(clientKey{}).(*observation); ok {
			c.mu.Lock()
			o.value.Transport = "plaintext-tcp"
			if IsLocal(r.Context()) {
				o.value.Transport = "local-ipc"
			} else if r.TLS != nil {
				o.value.Transport = "https"
			}
			o.value.MCP = o.value.MCP || r.URL.Path == "/mcp"
			o.value.UserAgent = boundedASCII(r.UserAgent(), 160)
			c.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

// ObserveBridge attaches client-reported incarnation metadata after the real
// MCP admission path has parsed it. It cannot change the connection transport.
func ObserveBridge(ctx context.Context, pid int, started string) {
	if pid <= 0 || len(started) > 64 {
		return
	}
	if o, ok := ctx.Value(clientKey{}).(*observation); ok {
		o.owner.mu.Lock()
		o.value.BridgePID, o.value.BridgeStarted = pid, boundedASCII(started, 64)
		o.owner.mu.Unlock()
	}
}

func boundedASCII(s string, limit int) string {
	if len(s) > limit {
		s = s[:limit]
	}
	b := []byte(s)
	for i, v := range b {
		if v < 32 || v > 126 {
			b[i] = '?'
		}
	}
	return string(b)
}

// Snapshot returns a deterministic copy of the currently connected clients.
func (c *Clients) Snapshot() []Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Client, 0, len(c.connections))
	for _, o := range c.connections {
		out = append(out, o.value)
	}
	slices.SortFunc(out, func(a, b Client) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out
}
