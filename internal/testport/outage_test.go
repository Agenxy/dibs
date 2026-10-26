// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package testport

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

func TestOutagePortIsNotAvailableToAutomaticSiblingBinds(t *testing.T) {
	address := Outage(t)
	RequireOutageAddress(t, address)
	for range 32 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		if listener.Addr().String() == address {
			t.Fatal("automatic sibling bind took the claimed outage port")
		}
	}
	// The fixture can still become a real server on the exact same address.
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

// A port in the band can be answered by a listener that is not part of the
// claim protocol: on macOS a wildcard listener does not stop the loopback
// bind that claiming performs, so only the dial sees it. Outage must move on
// to another port, not fail the test that asked for one. Measured on a hosted
// macOS gate, which failed an unrelated docs PR this way.
func TestOutageSkipsAPortAnsweredOutsideTheClaim(t *testing.T) {
	root := filepath.Join(os.TempDir(), "dibs-test-outage-ports")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// The first port no claim holds right now is the one Outage would take.
	var busy net.Listener
	for port := outageFirst; port <= outageLast && busy == nil; port++ {
		claim, _, err := claimOutagePort(root, port)
		if err != nil {
			t.Fatal(err)
		}
		if claim == nil {
			continue
		}
		paths.Unlock(claim)
		_ = claim.Close()
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		busy = listener
	}
	if busy == nil {
		t.Fatal("no free port in the outage band to occupy")
	}
	defer func() { _ = busy.Close() }()
	address := Outage(t)
	_, got, _ := net.SplitHostPort(address)
	_, occupied, _ := net.SplitHostPort(busy.Addr().String())
	if got == occupied {
		t.Fatalf("Outage returned %s, which a listener answers", address)
	}
	RequireOutageAddress(t, address)
}

func TestOutageClaimOtherProcess(t *testing.T) {
	parent := os.Getenv("DIBS_TEST_OUTAGE_PARENT")
	if parent == "" {
		return
	}
	address := Outage(t)
	if address == parent {
		t.Fatal("another process reused the still-claimed outage address")
	}
	t.Logf("separate-outage=%s", address)
}

func TestOutageClaimsRemainExclusiveAfterTheSocketCloses(t *testing.T) {
	address := Outage(t)
	cmd := exec.Command(os.Args[0], "-test.v", "-test.run=^TestOutageClaimOtherProcess$")
	cmd.Env = append(os.Environ(), "DIBS_TEST_OUTAGE_PARENT="+address)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "separate-outage=") {
		t.Fatalf("cross-process outage ownership: %v %s", err, output)
	}
}
