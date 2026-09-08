package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A dangling symlink is not an absence, and ensureCA must not treat it as one.
//
// os.Stat follows links, so a link whose target is gone reports ErrNotExist. An
// interrupted restore that leaves one dangling link and one missing file made
// BOTH halves look absent: ensureCA found nothing to refuse and generated a NEW
// signing identity, locking out every machine that had run `dibs trust` while
// reporting itself healthy.
//
// The reasoning was already written down in this package, in
// preflightCertReadable, which calls Lstat and says so at length. It was
// applied in one of the two places that need it. The dangling-symlink test
// drives that implementation and never reaches this one, so it stayed green
// while startup behaved differently. Found by the pre-release review.
//
// This test drives ensureCA itself, which is the whole point of it existing.
func TestEnsureCARefusesADanglingHalf(t *testing.T) {
	dir := t.TempDir()
	caCert := filepath.Join(dir, "ca-cert.pem")
	caKey := filepath.Join(dir, "ca-key.pem")

	// One dangling link, one genuinely absent file: what a half-finished
	// restore leaves behind.
	if err := os.Symlink(filepath.Join(dir, "gone.pem"), caCert); err != nil {
		t.Fatal(err)
	}
	// Setup must hold: Stat has to report the link as absent, or this test is
	// not exercising the case it names.
	if _, err := os.Stat(caCert); err == nil {
		t.Fatal("setup: the symlink resolves, so it is not dangling")
	}
	if _, err := os.Lstat(caCert); err != nil {
		t.Fatal("setup: the symlink itself is not there")
	}

	_, _, err := ensureCA(caCert, caKey)
	if err == nil {
		t.Fatal("ensureCA generated a NEW signing identity beside a dangling half. " +
			"Every machine pinned to the old one is locked out by a daemon that " +
			"then reports itself healthy, which is the silent rotation this file " +
			"refuses three other ways")
	}
	if !strings.Contains(err.Error(), caCert) {
		t.Errorf("the refusal does not name the file that needs attention: %v", err)
	}
	// And nothing was written: a refusal that still generated a key would have
	// done the damage it was refusing.
	if _, statErr := os.Lstat(caKey); statErr == nil {
		t.Error("a key was written despite the refusal")
	}
}

// And a genuinely empty directory is still a first run, or no board could ever
// start for the first time.
func TestEnsureCAStillGeneratesOnAFirstRun(t *testing.T) {
	dir := t.TempDir()
	cert, key, err := ensureCA(filepath.Join(dir, "ca-cert.pem"), filepath.Join(dir, "ca-key.pem"))
	if err != nil {
		t.Fatalf("a first run was refused: %v", err)
	}
	if cert == nil || key == nil {
		t.Error("a first run produced no signing identity")
	}
}

// A matching pair that is not a CA must be refused.
//
// ensureCA asked only whether the certificate and key matched and whether
// NotAfter had passed. A restored or misnamed SERVER certificate satisfies
// both, so `dibd -check` called the board healthy, the daemon signed leaves
// with something that is not a CA, and every client rejected the chain: a
// takeover reporting success while producing a board nobody can connect to.
// Found by the pre-release review.
func TestEnsureCARefusesALeafPretendingToBeACA(t *testing.T) {
	dir := t.TempDir()
	caCert := filepath.Join(dir, "ca-cert.pem")
	caKey := filepath.Join(dir, "ca-key.pem")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// A LEAF: no CA basic constraint, which is the whole difference.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "dibs board"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		IsCA:         false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	// The production writer, so the fixture is a file the daemon would produce.
	if err := writePEM(caCert, "CERTIFICATE", der, 0o600); err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePEM(caKey, "EC PRIVATE KEY", kb, 0o600); err != nil {
		t.Fatal(err)
	}

	// Setup must hold: the pair has to LOAD, or this refuses for the old reason
	// rather than the new one.
	if _, err := tls.LoadX509KeyPair(caCert, caKey); err != nil {
		t.Fatalf("setup: the pair does not load, so this tests the wrong branch: %v", err)
	}

	if _, _, err := ensureCA(caCert, caKey); err == nil {
		t.Error("a matching leaf pair was accepted as the board's signing identity. " +
			"Every leaf it signs is rejected by clients, and the daemon reports " +
			"itself healthy while serving a chain nobody accepts")
	}
}
