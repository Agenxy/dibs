// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

const outageFirst, outageLast = 20000, 29999

// Outage claims an address outside the kernel's automatic port range on
// macOS/Linux. Its socket is closed, so connects really return ECONNREFUSED,
// but the cross-process claim lasts until test cleanup. Sibling :0 listeners
// cannot be allocated this address during a long simulated outage.
// Other platforms retain the existing release-for-outage fixture contract.
func Outage(t testing.TB) string {
	t.Helper()
	first, last, supported, err := outageEphemeralRange()
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		return Reserve(t, "tcp", "127.0.0.1:0").ReleaseForOutage(t)
	}
	if !validOutageBand(first, last) {
		t.Fatalf("test outage band %d-%d overlaps configured ephemeral range %d-%d", outageFirst, outageLast, first, last)
	}
	root := filepath.Join(os.TempDir(), "dibs-test-outage-ports")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for port := outageFirst; port <= outageLast; port++ {
		claim, address, err := claimOutagePort(root, port)
		if err != nil {
			t.Fatal(err)
		}
		if claim == nil {
			continue
		}
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			// Something outside the claim protocol answers here. On macOS a
			// wildcard listener (":port", any process, IPv4 or dual-stack)
			// does not stop the loopback bind above, so only the dial sees
			// it. That port cannot simulate an outage: skip it, as a held
			// claim is skipped, rather than fail the caller's test.
			paths.Unlock(claim)
			_ = claim.Close()
			continue
		}
		t.Cleanup(func() { paths.Unlock(claim); _ = claim.Close() })
		return address
	}
	t.Fatal("no unoccupied cross-process claim in the test outage band")
	return ""
}

func validOutageBand(first, last int) bool {
	return first >= 1 && last <= 65535 && first <= last && (first > outageLast || last < outageFirst)
}

func claimOutagePort(root string, port int) (*os.File, string, error) {
	// Never unlink claim files: replacing an inode could split a live lock.
	// #nosec G304 -- fixed private temp root and a bounded numeric test-port filename.
	claim, err := os.OpenFile(filepath.Join(root, fmt.Sprint(port)+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", err
	}
	if err := paths.LockExclusive(claim, false); err != nil {
		_ = claim.Close()
		if paths.LockHeldElsewhere(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		_ = claim.Close()
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if err := listener.Close(); err != nil {
		_ = claim.Close()
		return nil, "", err
	}
	return claim, address, nil
}

// RequireOutageAddress checks a fixture's actual selected address, so changing
// its selection back to an ephemeral reservation fails before the outage starts.
func RequireOutageAddress(t testing.TB, address string) {
	t.Helper()
	_, _, supported, err := outageEphemeralRange()
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		return
	}
	_, raw, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < outageFirst || port > outageLast {
		t.Fatalf("outage fixture selected %s outside its claimed non-ephemeral band", address)
	}
}
