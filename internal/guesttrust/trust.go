// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package guesttrust states the address scope shared by guest issuance and the
// endpoint-scoped guest client. It carries no credential or global trust store.
package guesttrust

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/netip"
	"time"
)

// ScopeMatches is the exact production guest-CA shape, not a native-client
// acceptance claim. An empty excluded dNSName rejects all DNS names in Go.
func ScopeMatches(ca *x509.Certificate, ip netip.Addr) bool {
	if ca == nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" ||
		!ca.MaxPathLenZero || !ca.PermittedDNSDomainsCritical || len(ca.PermittedIPRanges) != 1 ||
		len(ca.ExcludedDNSDomains) != 1 || ca.ExcludedDNSDomains[0] != "" || len(ca.PermittedDNSDomains) != 0 {
		return false
	}
	rangeIP := ca.PermittedIPRanges[0]
	if rangeIP == nil {
		return false
	}
	ones, bits := rangeIP.Mask.Size()
	return ones == 128 && bits == 128 && rangeIP.IP.Equal(net.IP(ip.AsSlice()))
}

// ParseRoot validates one pinned public CA before any invitation-bearing I/O.
// The pin's provenance is the human's authenticated private handoff, not the
// fact that the same file also contains the PEM.
func ParseRoot(body []byte, ip netip.Addr, pin string, now time.Time) (*x509.Certificate, error) {
	block, rest := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("guest recipe must carry exactly one CA certificate; " +
			"ask the issuer for the original private recipe")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("guest CA is not a certificate; ask the issuer for the original private recipe")
	}
	if !ScopeMatches(ca, ip) || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("guest CA is not confined to the invited IPv6 with pathLen0 and all DNS excluded; " +
			"do not weaken its constraints")
	}
	if now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) || ca.CheckSignatureFrom(ca) != nil {
		return nil, errors.New("guest CA is expired, not yet valid or not self-signed; " +
			"check the clock and ask the issuer, never skip verification")
	}
	sum := sha256.Sum256(ca.RawSubjectPublicKeyInfo)
	if len(pin) != 64 || hex.EncodeToString(sum[:]) != pin {
		return nil, errors.New("guest CA SPKI does not match its private handoff pin; do not connect, ask the issuer")
	}
	return ca, nil
}
