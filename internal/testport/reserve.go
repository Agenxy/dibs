// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
func (r *Reservation) ReleaseForOutage(t testing.TB) string {
	t.Helper()
	port := r
	for attempt := 1; attempt <= 3; attempt++ {
		port.Release(t)
		conn, err := net.DialTimeout("tcp", port.Addr, time.Second)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return port.Addr
		}
		if err != nil {
			t.Fatalf("setup: outage fixture did not refuse connections: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("outage setup address occupied on attempt %d; selecting a fresh address", attempt)
		if attempt < 3 {
			host, _, err := net.SplitHostPort(port.Addr)
			if err != nil {
				t.Fatal(err)
			}
			port = Reserve(t, "tcp", net.JoinHostPort(host, "0"))
		}
	}
	t.Fatal("setup: another listener occupied all three outage addresses")
	return ""
}

// Bind keeps the address reserved through preparation and retries only a real
// address-in-use error from a same-process bind. Even the short release/bind gap
// collided during the full gate; parallel outbound connections can reuse an
// ephemeral port, so this environmental collision has a typed, bounded retry.
// Real-built daemon fixtures cannot use this: their exit loses the syscall error.
func Bind(t testing.TB, network, address string, prepare func(string), bind func() error) string {
	t.Helper()
	for attempt := 1; attempt <= 3; attempt++ {
		port := Reserve(t, network, address)
		prepare(port.Addr)
		port.Release(t)
		err := bind()
		if err == nil {
			return port.Addr
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("fixture bind failed without an address collision: %v", err)
		}
		t.Logf("fixture bind EADDRINUSE on attempt %d; selecting a fresh address", attempt)
	}
	t.Fatal("fixture bind EADDRINUSE on all three attempts")
	return ""
}
