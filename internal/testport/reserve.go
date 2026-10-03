// Package testport keeps ephemeral addresses owned during test fixture setup.
package testport

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Reservation owns a bound listener until Release is called.
// Release immediately before the fixture binds or starts its child; this is
// not an atomic socket handoff to another process. Real daemon fixtures fail
// loudly on any startup error: no distinct address-in-use exit status exists
// yet, so they cannot safely infer retryability from the daemon's prose.
type Reservation struct {
	Addr  string
	once  sync.Once
	close func() error
	err   error
}

// Reserve binds an ephemeral IPv4 or IPv6 loopback port and registers cleanup.
func Reserve(t testing.TB, network, address string) *Reservation {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	r := &Reservation{Addr: listener.Addr().String(), close: listener.Close}
	t.Cleanup(func() { r.Release(t) })
	return r
}

// Release gives up the reservation. Repeated cleanup calls are harmless.
func (r *Reservation) Release(t testing.TB) {
	t.Helper()
	r.once.Do(func() { r.err = r.close() })
	if r.err != nil {
		t.Fatal(r.err)
	}
}

// ReleaseForOutage makes the named outage fixtures' unavoidable ownership gap
// explicit, and verifies the connection-refused premise before testing retries.
// A held socket queues or times out on macOS instead of refusing connections.
func (r *Reservation) ReleaseForOutage(t testing.TB) {
	t.Helper()
	r.Release(t)
	conn, err := net.DialTimeout("tcp", r.Addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("setup: another listener took the outage fixture's address")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("setup: outage fixture did not refuse connections: %v", err)
	}
}
