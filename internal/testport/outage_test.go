// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package testport

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
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
