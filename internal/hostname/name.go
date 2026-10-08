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
// Only the first cold call waits. Expired values return immediately while one
// background lookup refreshes the cache, so board reads do not stall the writer.
func Name() string { return local.name() }

func (r *resolver) name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.initialized {
		r.value = r.resolve(r.value)
		r.expires = r.now().Add(cacheTTL)
		r.initialized = true
	} else if !r.now().Before(r.expires) && r.refreshing == nil {
		r.refreshing = make(chan struct{})
		go r.refresh(r.value, r.refreshing)
	}
	return r.value
}

func (r *resolver) refresh(previous string, done chan struct{}) {
	value := r.resolve(previous)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = value
	r.expires = r.now().Add(cacheTTL)
	r.refreshing = nil
	close(done)
}

func (r *resolver) resolve(previous string) string {
	ctx, cancel := context.WithTimeout(context.Background(), lookupBudget)
	defer cancel()
	value, err := r.lookup(ctx)
	value = strings.TrimSpace(value)
	if err == nil && value != "" {
		return value
	} else if previous == "" {
		value, err = r.fallback()
		if err == nil {
			return strings.TrimSpace(value)
		}
	}
	return previous
}
