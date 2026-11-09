// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net"
	"net/netip"
	"testing"
)

// The guest listener hands a client the CA it is pinned to, beside the leaf.
//
// A guest pins the board by the SHA-256 of its CA's public key, and is told
// that pin out of band (the invite, or nearby pairing on the phone). With only
// the leaf on the wire, a client that has the pin and nothing else has no
// certificate to hash: it would have to carry the CA itself, which is the
// thing the pin exists to avoid shipping. iOS evaluates server trust from the
// chain the server sends, so the phone could not check its pin at all. What
// is asserted is what crosses a real handshake, not what the config holds.
func TestTheGuestListenerServesTheCAItsPinNames(t *testing.T) {
	config, _, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer func() { _ = server.Close(); _ = client.Close() }()
	go func() { _ = tls.Server(server, config).Handshake() }()

	var chain []*x509.Certificate
	conn := tls.Client(client, &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Verification is by pin below, exactly as a pinned guest does it.
		InsecureSkipVerify: true, // #nosec G402 -- the test verifies the pin itself
		VerifyConnection: func(cs tls.ConnectionState) error {
			chain = cs.PeerCertificates
			return nil
		},
	})
	if err := conn.Handshake(); err != nil {
		t.Fatal("setup: handshake", err)
	}
	if len(chain) != 2 {
		t.Fatalf("the guest listener sent %d certificates; a pinned client needs the leaf and the CA", len(chain))
	}
	spki := sha256.Sum256(chain[1].RawSubjectPublicKeyInfo)
	if hex.EncodeToString(spki[:]) != pin {
		t.Fatal("the second certificate is not the CA the advertised pin names")
	}
	if !chain[1].IsCA {
		t.Fatal("the certificate the pin names is not a CA")
	}
	if err := chain[0].CheckSignatureFrom(chain[1]); err != nil {
		t.Fatalf("the pinned CA did not sign the leaf it was served with: %v", err)
	}
}
