package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// A configured certificate is checked against the address actually bound.
//
// boardconfig checks it against `addr` in dibs.toml, and `-addr` and DIBS_ADDR
// both outrank that file. A board with an explicit pair and no configured
// address therefore passed `dibd -check` and config loading, served TLS on the
// default loopback listener, and was refused by every client on hostname
// verification: the silent, total, all-at-once failure the managed path renews
// and re-issues to avoid, on the one path that had no such check.
//
// boardconfig cannot answer this. It cannot see the flag or the variable, and
// assuming loopback there would refuse a certificate that is correct for the
// address the daemon was told to bind, which `dibs upgrade` always passes: that
// refusal would land mid-cutover with the previous daemon already stopped.
func TestAConfiguredCertificateMustNameTheAddressBeingServed(t *testing.T) {
	loopback := certFor(t, []string{"127.0.0.1"})
	elsewhere := certFor(t, []string{"10.0.0.9"})
	named := certFor(t, nil, "hub.example")

	for _, tc := range []struct {
		name, addr string
		cert       tls.Certificate
		wantErr    bool
	}{
		{
			"the default listener, and the certificate names it",
			"127.0.0.1:4777", loopback, false,
		},
		{
			"the default listener, and the certificate names somewhere else",
			"127.0.0.1:4777", elsewhere, true,
		},
		{
			"a LAN address the flag chose, and the certificate names it",
			"10.0.0.9:4777", elsewhere, false,
		},
		{
			"a LAN address the flag chose, and the certificate names loopback",
			"10.0.0.9:4777", loopback, true,
		},
		{
			"a hostname listener, and the certificate names it",
			"hub.example:4777", named, false,
		},
		// A wildcard serves whatever was dialled, so no certificate can name it
		// in advance and refusing one would be a guess.
		{
			"a wildcard bind, which nothing can be verified against",
			"0.0.0.0:4777", elsewhere, false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := certificateNamesListener(tc.cert, tc.addr)
			if tc.wantErr && err == nil {
				t.Errorf("serving %s with this certificate was accepted. It will serve, "+
					"and every client dialling that address refuses it: the board is up "+
					"and unreachable, and nothing said so", tc.addr)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("serving %s with this certificate was refused: %v", tc.addr, err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "reissue") &&
				!strings.Contains(err.Error(), "Reissue") {
				t.Errorf("the refusal does not say what to do about it: %v", err)
			}
		})
	}

	// And no pair configured is not a fault: the managed path handles it.
	if err := certificateNamesListener(tls.Certificate{}, "127.0.0.1:4777"); err != nil {
		t.Errorf("a board with no explicit certificate was refused: %v", err)
	}
}

// certFor builds a self-signed leaf naming the given IPs and DNS names.
func certFor(t *testing.T, ips []string, dns ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "dibs test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dns,
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}}
}
