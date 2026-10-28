// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"sync"
	"time"
)

// The first register must not wait on a process-table walk. Discovery starts
// with the bridge and a request omits this additive evidence after 500ms. A
// failed inventory stays unknown and a later request can retry it; it never
// becomes a claim that the bridge is outside the app.
const bridgeSurfaceBudget = 500 * time.Millisecond

type surfaceProbe struct {
	mu      sync.Mutex
	ctx     context.Context
	surface string
	known   bool
	pending chan struct{}
}

var bridgeAncestry = &surfaceProbe{}

func primeBridgeAncestry(ctx context.Context) {
	if remoteSession {
		return
	}
	bridgeAncestry.ctx = ctx
	bridgeAncestry.prime()
}

func (p *surfaceProbe) prime() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known {
		done := make(chan struct{})
		close(done)
		return done
	}
	if p.pending == nil {
		ch := make(chan struct{})
		p.pending = ch
		ctx := p.ctx
		if ctx == nil {
			ctx = context.Background() // direct enrichment callers; runBridge supplies the session
		}
		lookup := detectedSurface
		go func() {
			surface, err := lookup(ctx)
			p.mu.Lock()
			if err == nil {
				p.surface, p.known = surface, true
			}
			p.pending = nil
			p.mu.Unlock()
			close(ch)
		}()
	}
	return p.pending
}

func (p *surfaceProbe) result() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.surface, p.known
}
