// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/transport"
)

// One selection binds address, trust and credentials. Reading just the new
// secret while retaining the old endpoint sends one board's secret to another.
// The fixed service pin is read per attempt; Supgang resolution is still only
// on a directory change or refused dial, never on the unchanged happy path.
type bridgeRoute struct {
	mu          sync.Mutex
	dir, origin string
	generation  uint64 // changes when address selection is explicitly refreshed
}

func newBridgeRoute() (*bridgeRoute, error) {
	r := &bridgeRoute{}
	_, _, err := r.current(false)
	return r, err
}

func (r *bridgeRoute) current(refresh bool) (string, string, error) {
	selected, err := paths.SelectDir(false)
	if err != nil {
		return "", "", err
	}
	if err := configReadable(selected.Dir); err != nil {
		return "", "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if selected.Dir != r.dir || refresh {
		r.dir, r.origin = selected.Dir, boardOriginIn(selected.Dir)
		r.generation++
	}
	return r.dir, r.origin, nil
}

type bridgeDirectoryTransport struct {
	route *bridgeRoute
	mu    sync.Mutex
	dir   string
	next  http.RoundTripper
	idle  interface{ CloseIdleConnections() }
}

func bridgeDaemonClient(route *bridgeRoute, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &bridgeDirectoryTransport{route: route},
		CheckRedirect: localtransport.RefuseRedirect}
}

func (t *bridgeDirectoryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	dir, origin, err := t.route.current(false)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(origin)
	if err != nil {
		return nil, err
	}
	next := req.Clone(req.Context())
	next.URL.Scheme, next.URL.Host, next.URL.User = base.Scheme, base.Host, base.User
	// A fresh selection does not inherit the previous board's TLS roots. Keep
	// only this directory's transport; a later return rebuilds from disk.
	t.mu.Lock()
	if t.next == nil || dir != t.dir {
		if t.idle != nil {
			t.idle.CloseIdleConnections()
		}
		rt := http.DefaultTransport
		if base, ok := rt.(*http.Transport); ok {
			rt = base.Clone()
		}
		// The guarded transport verifies this selected directory before reading
		// its trust and credential as one profile snapshot.
		t.dir = dir
		guard := &guardedTransport{next: rt, dir: dir}
		t.idle = guard
		t.next = transport.Stamp(guard)
	}
	roundTripper := t.next
	t.mu.Unlock()
	return roundTripper.RoundTrip(next)
}

func rediscoverBoard(client *http.Client, previous *url.URL) (string, *http.Client, error) {
	if bridge, ok := client.Transport.(*bridgeDirectoryTransport); ok {
		_, origin, err := bridge.route.current(true)
		if err != nil {
			return "", nil, err
		}
		return originWithPath(origin, previous), bridgeDaemonClient(bridge.route, client.Timeout), nil
	}
	where := boardNow(previous)
	if previous == nil || where != previous.String() {
		client = daemonClient(client.Timeout)
	}
	return where, client, nil
}

func originWithPath(origin string, previous *url.URL) string {
	if previous == nil {
		return origin
	}
	path := previous.EscapedPath()
	if previous.RawQuery != "" {
		path += "?" + previous.RawQuery
	}
	return origin + path
}
