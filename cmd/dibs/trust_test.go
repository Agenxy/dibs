package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fingerprint an operator can compare against any other tool's.
//
// The value is worth nothing if it is ours alone: the whole procedure is "read
// it on one machine, read it on the other, check they match", and the second
// reading is often `openssl x509 -fingerprint -sha256`.
func TestFingerprintMatchesTheConventionalForm(t *testing.T) {
	der := selfSigned(t, time.Now().Add(24*time.Hour))
	got := fingerprint(der)

	if n := strings.Count(got, ":"); n != 31 {
		t.Errorf("got %d separators, want 31: a SHA-256 fingerprint is 32 colon-separated bytes\n%s", n, got)
	}
	if got != strings.ToUpper(got) {
		t.Errorf("fingerprint is not upper-case hex, so it will not compare by eye: %s", got)
	}
	if fingerprint(der) != got {
		t.Error("fingerprint is not stable for the same certificate")
	}
	if fingerprint(selfSigned(t, time.Now().Add(48*time.Hour))) == got {
		t.Error("two different certificates share a fingerprint")
	}
}

// Trusting one daemon must not trust everything.
//
// trustedPool adds to the SYSTEM roots so a daemon behind a real certificate
// keeps working. The risk in that is the opposite mistake: returning a usable
// pool when nothing has been trusted, which would silently accept any
// certificate the system happens to like on an address the operator believes is
// pinned.
func TestAnUntrustedMachineGetsNoPool(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())

	if pool := trustedPool(); pool != nil {
		t.Error("a machine that has trusted nothing got a certificate pool: " +
			"the pin is not gating anything")
	}
}

// And a machine that HAS trusted something gets a pool carrying it.
func TestATrustedCertificateIsInThePool(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	der := selfSigned(t, time.Now().Add(24*time.Hour))
	blob := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, "trusted-certs.pem"), blob, 0o600); err != nil {
		t.Fatal(err)
	}

	pool := trustedPool()
	if pool == nil {
		t.Fatal("a trusted certificate produced no pool, so trusting it did nothing")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Errorf("the trusted certificate does not verify against the pool it is in: %v", err)
	}
}

// selfSigned returns the DER of a throwaway certificate.
func selfSigned(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "dibs-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
