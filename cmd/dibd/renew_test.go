package main

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

func serialOf(t *testing.T, c *tls.Certificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.String()
}

// A leaf inside its renewal window is re-issued on the next handshake, under
// the same CA; a fresh one is served as it is. The daemon used to install the
// startup leaf for good.
func TestTheBoardsCertificateIsRenewedWhileItRuns(t *testing.T) {
	dir := t.TempDir()
	const addr = "127.0.0.1:4270"
	old := leafLifetime
	leafLifetime = 2 * time.Hour // already inside the 30-day renewal window
	t.Cleanup(func() { leafLifetime = old })
	certFile, keyFile, err := ensureSelfSignedCert(dir, addr)
	if err != nil {
		t.Fatal("setup:", err)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal("setup:", err)
	}
	first := serialOf(t, &pair)
	r := newRenewingLeaf(dir, addr, pair)
	got, err := r.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if serialOf(t, got) == first {
		t.Fatal("a leaf inside its renewal window was served as it was: an uninterrupted " +
			"daemon serves an expired certificate that the README says is replaced")
	}
	again, _ := r.get(nil)
	if serialOf(t, again) != serialOf(t, got) {
		t.Error("the leaf was re-issued again on the very next handshake: renewal is not rate-limited")
	}

	// A fresh leaf is left alone.
	leafLifetime = old
	fresh := t.TempDir()
	certFile, keyFile, err = ensureSelfSignedCert(fresh, addr)
	if err != nil {
		t.Fatal("setup:", err)
	}
	pair, err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal("setup:", err)
	}
	r = newRenewingLeaf(fresh, addr, pair)
	if got, _ := r.get(nil); serialOf(t, got) != serialOf(t, &pair) {
		t.Error("a fresh leaf was re-issued: every handshake would mint a certificate")
	}
}

// The daemon's own leaf is served through the renewal; an operator's is not
// the daemon's to renew.
func TestOnlyTheBoardsOwnCertificateIsRenewed(t *testing.T) {
	dir := t.TempDir()
	const addr = "127.0.0.1:4270"
	tr, err := resolveTransport(dir, addr, "https", Config{})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if !tr.selfIssued {
		t.Fatal("a board with no certificate configured did not report issuing its own: " +
			"the leaf it made would be installed for good")
	}
	pair, err := tls.LoadX509KeyPair(tr.certFile, tr.keyFile)
	if err != nil {
		t.Fatal("setup:", err)
	}
	if cfg := tlsConfigFor(tr, dir, addr, pair); cfg.GetCertificate == nil || len(cfg.Certificates) != 0 {
		t.Error("the board's own leaf is installed for good instead of served through the renewal")
	}
	theirs := transport{certFile: tr.certFile, keyFile: tr.keyFile}
	if cfg := tlsConfigFor(theirs, dir, addr, pair); cfg.GetCertificate != nil || len(cfg.Certificates) != 1 {
		t.Error("an operator's certificate would be re-issued under the board CA")
	}
}
