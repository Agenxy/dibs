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
	mu       sync.Mutex
	value    string
	expires  time.Time
	lookup   func(context.Context) (string, error)
	fallback func() (string, error)
	now      func() time.Time
}

var local = resolver{lookup: platformName, fallback: os.Hostname, now: time.Now}

// Name returns the current display name, with a brief cache. A failed refresh
// keeps the last good label; the kernel hostname is only the initial fallback.
func Name() string { return local.name() }

func (r *resolver) name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.now().Before(r.expires) {
		return r.value
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupBudget)
	defer cancel()
	value, err := r.lookup(ctx)
	value = strings.TrimSpace(value)
	if err == nil && value != "" {
		r.value = value
	} else if r.value == "" {
		value, err = r.fallback()
		if err == nil {
			r.value = strings.TrimSpace(value)
		}
	}
	r.expires = r.now().Add(cacheTTL)
	return r.value
}
