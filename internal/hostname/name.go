// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package hostname reads a display label for this computer. This is not an
// identity or evidence for a host comparison: callers keep their host IDs and
// any legacy kernel-name comparisons separately.
package hostname

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	cacheTTL     = 5 * time.Second
	lookupBudget = 100 * time.Millisecond
)

type resolver struct {
	mu          sync.Mutex
	value       string
	expires     time.Time
	initialized bool
	refreshing  chan struct{}
	lookup      func(context.Context) (string, error)
	fallback    func() (string, error)
	now         func() time.Time
}

var local = resolver{lookup: platformName, fallback: os.Hostname, now: time.Now}

// Name returns the current display name, with a brief cache. A failed refresh
// keeps the last good label; the kernel hostname is only the initial fallback.
// Cold callers wait for one bounded lookup. Use Current on the writer loop.
func Name() string { return local.name() }

// Label distinguishes a successful friendly lookup from the kernel fallback.
type Label struct {
	Name     string
	Fallback bool
}

// Current returns immediately, starting at most one background lookup when
// needed. A fallback never counts as a successful cache entry: the next call
// retries after a failed lookup, without waiting for the friendly cache's TTL.
func Current() Label { return local.current() }

func (r *resolver) read() (string, bool, chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (!r.initialized || !r.now().Before(r.expires)) && r.refreshing == nil {
		r.refreshing = make(chan struct{})
		go r.refresh(r.refreshing)
	}
	return r.value, !r.initialized, r.refreshing
}

func (r *resolver) name() string {
	value, cold, done := r.read()
	if cold {
		<-done
		r.mu.Lock()
		value, cold = r.value, !r.initialized
		r.mu.Unlock()
	}
	return r.label(value, cold).Name
}

func (r *resolver) current() Label {
	value, cold, _ := r.read()
	return r.label(value, cold)
}

func (r *resolver) label(value string, cold bool) Label {
	if cold {
		fallback, err := r.fallback()
		if err == nil {
			value = strings.TrimSpace(fallback)
		}
	}
	return Label{Name: value, Fallback: cold}
}

func (r *resolver) refresh(done chan struct{}) {
	value, ok := r.resolve()
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.value = value
		r.initialized = true
	}
	r.expires = r.now().Add(cacheTTL)
	r.refreshing = nil
	close(done)
}

func (r *resolver) resolve() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), lookupBudget)
	defer cancel()
	value, err := r.lookup(ctx)
	value = strings.TrimSpace(value)
	if err == nil && value != "" {
		return value, true
	}
	return "", false
}
