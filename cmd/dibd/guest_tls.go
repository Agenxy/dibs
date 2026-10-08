// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/guesttrust"
)

// The DNS constraint encoding has one choice. Any change needs a new guest
// trust ceremony and the same all-DNS negative matrix, not a silent fallback.
const guestExcludedDNS = ""

// A guest signing identity is NOT the fleet's identity. It is scoped to one
// exact operator-selected address. Changing that scope requires a new trust
// ceremony, not silent rotation at startup. Neither file is ever overwritten.
func ensureGuestCA(dir string, ip netip.Addr) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if !ip.Is6() || ip.Is4In6() || ip.Zone() != "" {
		return nil, nil, errors.New("guest CA requires one unzoned IPv6 address; choose an assigned global public-ip")
	}
	certPath, keyPath := filepath.Join(dir, "guest-ca.pem"), filepath.Join(dir, "guest-ca-key.pem")
	pair, loadErr := tls.LoadX509KeyPair(certPath, keyPath)
	if loadErr == nil {
		ca, parseErr := x509.ParseCertificate(pair.Certificate[0])
		key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
		if err := validSigningIdentity(ca, parseErr, ok, certPath, keyPath); err != nil {
			// Reuse the fleet's validation, not its recovery ceremony: this root
			// belongs only to invited guests, never `dibs trust` fleet members.
			return nil, nil, errors.New("guest signing identity failed certificate/key validation " +
				"(malformed, mismatched, expired, not-yet-valid or not a signing CA); restore both guest-ca files, " +
				"or revoke old invitations and explicitly archive those files before re-inviting with fresh trust material; " +
				"never rotate the fleet tls-ca files")
		}
		if !guestScopeMatches(ca, ip) {
			return nil, nil, errors.New("guest CA address scope or constraints changed; restore its original public-ip, " +
				"or revoke old invitations and explicitly archive both guest-ca files before re-inviting guests with a new CA " +
				"(never remove the fleet tls-ca files)")
		}
		return ca, key, nil
	}
	for _, path := range []string{certPath, keyPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("guest signing identity cannot be loaded: %w; restore both guest-ca files, "+
				"do not silently replace guests' pinned identity", loadErr)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "dibs guest CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(caLifetime),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:     true, BasicConstraintsValid: true, MaxPathLenZero: true,
		PermittedDNSDomainsCritical: true,
		PermittedIPRanges:           []*net.IPNet{{IP: net.IP(ip.AsSlice()), Mask: net.CIDRMask(128, 128)}},
		// Empty dNSName subtree excludes every DNS name in Go's verifier.
		// This is a candidate encoding, NOT native-client acceptance. No native
		// client recipe is offered until the actual build passes the malicious
		// leaf matrix, including a mixed intended-IP/unrelated-DNS leaf.
		ExcludedDNSDomains: []string{guestExcludedDNS},
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	// O_EXCL handles first-run races without replacing a surviving identity.
	if err := writeGuestIdentity(dir, "guest-ca-key.pem", "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return nil, nil, err
	}
	if err := writeGuestIdentity(dir, "guest-ca.pem", "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	parsed, err := x509.ParseCertificate(der)
	return parsed, key, err
}

func writeGuestIdentity(dir, name, kind string, der []byte, mode os.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}

func guestScopeMatches(ca *x509.Certificate, ip netip.Addr) bool {
	return guesttrust.ScopeMatches(ca, ip)
}

// Derive the IP-only leaf in memory. No stale on-disk leaf can outlive scope
// validation; fleet leaf files and keys are never touched here.
func guestTLS(dir string, ip netip.Addr) (*tls.Config, string, string, error) {
	ca, signer, err := ensureGuestCA(dir, ip)
	if err != nil {
		return nil, "", "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", "", err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, "", "", err
	}
	now := time.Now()
	leaf := &x509.Certificate{
		SerialNumber: serial, NotBefore: now.Add(-time.Hour), NotAfter: minTime(now.Add(leafLifetime), ca.NotAfter),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IP(ip.AsSlice())}, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, signer)
	if err != nil {
		return nil, "", "", err
	}
	pair := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	// Verify what is about to be served, not just the presence of extensions.
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", "", err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = parsed.Verify(x509.VerifyOptions{Roots: roots, DNSName: ip.String()}); err != nil {
		return nil, "", "", fmt.Errorf("guest TLS leaf is unverifiable: %w; "+
			"inspect guest CA before enabling the listener", err)
	}
	pair.Leaf = parsed
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	pin := sha256.Sum256(ca.RawSubjectPublicKeyInfo)
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	return config, string(caPEM), hex.EncodeToString(pin[:]), nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
