package main

import (
	"net/netip"
	"strings"
	"testing"
)

// The production parser is the door. A helper called only by a fixture would
// prove nothing about whether an operator can configure this listener.
func TestPublicIPRequiresExplicitUnverifiedClientAcknowledgement(t *testing.T) {
	fs := cloudFlagSet()
	opts, _ := registerDaemonFlags(fs)
	if err := fs.Parse([]string{"--public-ip", "2600:1700::1"}); err != nil {
		t.Fatalf("public IP mode is unreachable through the production parser: %v", err)
	}
	_, err := resolvePublic(opts)
	if err == nil || !strings.Contains(err.Error(), "verified") {
		t.Fatalf("unverified native-client trust was silently accepted: %v", err)
	}
}

func TestPublicIPRefusesNonGlobalIPv6AndAmbiguousModes(t *testing.T) {
	for _, args := range [][]string{
		{"--public-ip", "::1"},
		{"--public-ip", "fe80::1"},
		{"--public-ip", "fc00::1"},
		{"--public-ip", "192.0.2.1"},
		{"--public-ip", "2001:db8::1"},
		{"--public-ip", "board.example.com"},
		{"--public-ip", "2600:1700::1", "--public-host", "board.example.com"},
		{"--public-ip", "2600:1700::1", "--public-url", "https://board.example.com"},
		{"--public-ip", "2600:1700::1", "--public-addr", "[::]:4778"},
		{"--public-ip", "2600:1700::1", "--public-addr", "[2600:1700::2]:4778"},
	} {
		fs := cloudFlagSet()
		opts, _ := registerDaemonFlags(fs)
		args = append(args, "--ack-unverified-guest-client")
		if err := fs.Parse(args); err != nil {
			t.Fatalf("setup parser: %v", err)
		}
		if _, err := resolvePublic(opts); err == nil {
			t.Fatalf("unsafe/ambiguous guest listener accepted: %v", args)
		}
	}
}

func TestPublicIPClassificationIsNotStabilityOrAssignment(t *testing.T) {
	for _, ip := range []string{"::1", "::", "::ffff:192.0.2.1", "fe80::1", "fc00::1", "ff02::1", "2001:db8::1", "2001:2::1", "3fff::1", "2600:1700::1%en0"} {
		if globalGuestIPv6(netip.MustParseAddr(ip)) {
			t.Fatalf("non-public scope accepted: %s", ip)
		}
	}
	for _, ip := range []string{"2600:1700::1", "2001:4860::1"} {
		if !globalGuestIPv6(netip.MustParseAddr(ip)) {
			t.Fatalf("ordinary global scope refused: %s", ip)
		}
	}
	if err := assignedGuestIP(netip.MustParseAddr("::1")); err != nil {
		t.Fatalf("loopback fixture assignment measurement failed: %v", err)
	}
	if err := assignedGuestIP(netip.MustParseAddr("2001:db8::dead:beef")); err == nil {
		t.Fatal("unassigned documentation fixture appeared assigned")
	}
}
