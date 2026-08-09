//go:build unix

package liveness

import (
	"syscall"
)

// Poller answers "is this process alive?" for lane PID bindings, the coarsest
// of the questions this package handles. Portable: kill(pid, 0). The engine's
// sweep records its verdicts into the ledger so replay never re-probes.
type Poller struct{}

// New returns the platform prober.
func New() *Poller { return &Poller{} }

// Alive reports whether pid exists (EPERM counts as alive: it exists but
// belongs to another user).
func (p *Poller) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil || err == syscall.EPERM {
		return true
	}
	return false
}
