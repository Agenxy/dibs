// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGuestCAActuallyRejectsMaliciousNamesAndIntermediateBypass(t *testing.T) {
	dir := t.TempDir()
	ip := netip.MustParseAddr("2600:1700::1")
	ca, key, err := ensureGuestCA(dir, ip)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, tc := range []struct {
		name, verify string
		ips          []net.IP
		dns          []string
		accept       bool
	}{
		{"intended", ip.String(), []net.IP{net.ParseIP(ip.String())}, nil, true},
		{"other-v6", "2600:1700::2", []net.IP{net.ParseIP("2600:1700::2")}, nil, false},
		{"v4", "192.0.2.1", []net.IP{net.ParseIP("192.0.2.1")}, nil, false},
		{"dns", "unrelated.example", nil, []string{"unrelated.example"}, false},
		{"single-label-dns", "localhost", nil, []string{"localhost"}, false},
		{"reserved-dns", "invalid", nil, []string{"invalid"}, false},
		{"reserved-subdomain", "anything.invalid", nil, []string{"anything.invalid"}, false},
		{"mixed-ip-dns", ip.String(), []net.IP{net.ParseIP(ip.String())}, []string{"unrelated.example"}, false},
		{"mixed-in-out-ip", ip.String(), []net.IP{net.ParseIP(ip.String()), net.ParseIP("2600:1700::2")}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf := signGuestFixture(t, ca, key, &x509.Certificate{IPAddresses: tc.ips, DNSNames: tc.dns}, &key.PublicKey)
			_, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: tc.verify})
			if (err == nil) != tc.accept {
				t.Fatalf("verification %s: %v", tc.name, err)
			}
		})
	}
	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	intermediate := signGuestFixture(t, ca, key, &x509.Certificate{IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, &intermediateKey.PublicKey)
	leaf := signGuestFixture(t, intermediate, intermediateKey, &x509.Certificate{IPAddresses: []net.IP{net.ParseIP(ip.String())}}, &key.PublicKey)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermediate)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, DNSName: ip.String()}); err == nil {
		t.Fatal("intermediate bypassed path-length-zero root")
	}
}

func signGuestFixture(t *testing.T, parent *x509.Certificate, signer *ecdsa.PrivateKey, template *x509.Certificate, public any) *x509.Certificate {
	t.Helper()
	serial, err := newSerial()
	if err != nil {
		t.Fatal(err)
	}
	template.SerialNumber = serial
	template.NotBefore = time.Now().Add(-time.Hour)
	template.NotAfter = time.Now().Add(time.Hour)
	if !template.IsCA {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.KeyUsage = x509.KeyUsageDigitalSignature
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, public, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestGuestCARefusesCorruptionPartialIdentityAndScopeChangeWithoutOverwriting(t *testing.T) {
	for _, mode := range []string{"scope-change", "bad-cert", "missing-cert", "dangling-cert"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ip := netip.MustParseAddr("2600:1700::1")
			if _, _, err := ensureGuestCA(dir, ip); err != nil {
				t.Fatal(err)
			}
			keyPath, certPath := filepath.Join(dir, "guest-ca-key.pem"), filepath.Join(dir, "guest-ca.pem")
			before, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "scope-change":
				ip = netip.MustParseAddr("2600:1700::2")
			case "bad-cert":
				if err := os.WriteFile(certPath, []byte("broken"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing-cert":
				if err := os.Remove(certPath); err != nil {
					t.Fatal(err)
				}
			case "dangling-cert":
				if err := os.Remove(certPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, "absent"), certPath); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := ensureGuestCA(dir, ip); err == nil {
				t.Fatal("identity replacement was silently permitted")
			}
			after, err := os.ReadFile(keyPath)
			if err != nil || string(before) != string(after) {
				t.Fatalf("surviving guest key overwritten: %v", err)
			}
		})
	}
}

func TestGuestLeafHasOnlyIntendedIPAndSeparateRoot(t *testing.T) {
	dir := t.TempDir()
	ip := netip.MustParseAddr("::1")
	config, pemBytes, pin, err := guestTLS(dir, ip)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(config.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.IsCA || leaf.Subject.CommonName != "" || len(leaf.DNSNames) != 0 || len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.IPv6loopback) {
		t.Fatalf("unexpected guest leaf: %#v", leaf)
	}
	if len(pin) != 64 || pemBytes == "" || config.MinVersion != tls.VersionTLS13 {
		t.Fatal("missing TLS trust metadata")
	}
	_, _, _, err = guestTLS(dir, ip)
	if err != nil {
		t.Fatal(err)
	}
}
