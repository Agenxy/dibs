// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package folderaccess measures protected-folder reads outside the replayable
// state machine. Observations are disposable and never promise an OS grant.
package folderaccess

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Settings is the human-owned macOS protected-folder consent surface.
const Settings = "System Settings → Privacy & Security → Files & Folders → Dibs"

// Observation is read evidence; requesting a read never implies a grant.
type Observation struct {
	Folder     string    `json:"folder"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	Requested  bool      `json:"requested"`
	ObservedAt time.Time `json:"observed_at"`
}

type root struct {
	name, path  string
	observation Observation
	running     bool
	measured    bool
	logged      bool
}

// Monitor bounds directory probes independently of the replayable state.
type Monitor struct {
	mu     sync.Mutex
	roots  []*root
	read   func(string) error
	budget time.Duration
}

// New takes a directory-read dependency so daemon integration tests can drive
// blocked/denied outcomes without requesting real OS consent. nil reads the
// real directory. No entries or file contents are retained.
func New(home string, read func(string) error) *Monitor {
	// The agent's cwd has already passed filesystem canonicalisation. Resolve
	// the home directory itself (not the protected roots) so /var and
	// /private/var, or a symlinked home, cannot hide a needed probe.
	if canonical, err := filepath.EvalSymlinks(home); err == nil {
		home = canonical
	}
	if read == nil {
		read = readDirectory
	}
	m := &Monitor{read: read, budget: 250 * time.Millisecond}
	for _, name := range []string{"Desktop", "Documents", "Downloads"} {
		m.roots = append(m.roots, &root{name: name, path: filepath.Join(home, name)})
	}
	return m
}

func readDirectory(path string) error {
	// #nosec G304 -- one of the user's three fixed protected roots, not a participant path
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.ReadDir(1)
	if errors.Is(err, io.EOF) {
		return nil
	} // empty readable directories are granted
	return err
}

// Directory starts at most one read for each needed root, without waiting on
// the filesystem. The caller must establish that dir belongs to this machine.
func (m *Monitor) Directory(dir string) {
	if m == nil || !filepath.IsAbs(dir) {
		return
	}
	for _, r := range m.roots {
		rel, err := filepath.Rel(r.path, dir)
		if err == nil && filepath.IsLocal(rel) {
			m.start(r, false)
		}
	}
}

// Request is an explicit acquisition attempt for the three fixed roots. It
// retries only ungranted roots and never starts a second blocked kernel read.
// Returning true means a read was requested, never that a dialog appeared.
func (m *Monitor) Request() bool {
	if m == nil {
		return false
	}
	requested := false
	for _, r := range m.roots {
		requested = m.start(r, true) || requested
	}
	return requested
}

func (m *Monitor) start(r *root, explicit bool) bool {
	m.mu.Lock()
	if r.running || (r.measured && (!explicit || r.observation.State == "granted")) {
		m.mu.Unlock()
		return false
	}
	r.running, r.measured = true, true
	r.observation = Observation{Folder: r.name, State: "pending", Requested: explicit, ObservedAt: time.Now()}
	m.mu.Unlock()
	// One worker may remain blocked in open/read forever. A second small
	// goroutine owns the bound and exits; no timer re-arms or retries a read.
	done := make(chan struct{})
	go func() { m.finish(r, m.read(r.path)); close(done) }()
	go func() {
		timer := time.NewTimer(m.budget)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
			m.mu.Lock()
			var observation Observation
			if r.running {
				r.observation.Reason = "directory read has not returned; OS consent may be pending"
				observation = takeLogLocked(r)
			}
			m.mu.Unlock()
			logObservation(observation)
		}
	}()
	return true
}

func (m *Monitor) finish(r *root, err error) {
	m.mu.Lock()
	r.running = false
	r.observation.ObservedAt = time.Now()
	switch {
	case err == nil:
		r.observation.State, r.observation.Reason = "granted", "directory read completed"
	case errors.Is(err, os.ErrPermission):
		r.observation.State, r.observation.Reason = "denied", "directory read was refused"
	default:
		r.observation.State, r.observation.Reason = "unknown", "directory read could not establish access"
	}
	observation := takeLogLocked(r)
	m.mu.Unlock()
	logObservation(observation)
}

func takeLogLocked(r *root) Observation {
	if r.logged {
		return Observation{}
	}
	r.logged = true
	return r.observation
}

func logObservation(o Observation) {
	if o.Folder == "" {
		return
	}
	slog.Info("Dibs protected-folder access", "folder", o.Folder, "state", o.State, "reason", o.Reason, "fix", Settings)
}

// Snapshot returns the last derived evidence without waiting for a reader.
func (m *Monitor) Snapshot() []Observation {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Observation
	for _, r := range m.roots {
		if r.measured {
			out = append(out, r.observation)
		}
	}
	return out
}

// Advice explains ungranted observations without changing OS consent.
func Advice(observations []Observation) string {
	for _, o := range observations {
		if o.State != "granted" {
			return "Dibs has not confirmed access to a folder its local agents use. " +
				"Check " + Settings + ". A requested read does " +
				"not confirm a grant; changing source/release signing channel can require access again."
		}
	}
	return ""
}
