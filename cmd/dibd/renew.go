package main

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"sync"
	"time"
)

// leafLifetime is how long a leaf the daemon issues itself is good for. A
// variable so a test can issue one that is already inside the renewal window.
var leafLifetime = certLifetime

// renewingLeaf serves the board's own certificate and re-issues it under the
// board CA when it nears expiry, so a daemon that is never restarted never
// serves an expired leaf.
//
// The README promised this for as long as the board CA existed, and the
// daemon issued the leaf once at startup and installed it for good: an
// uninterrupted year, and every client's next connection failed on an
// expired certificate they had been told would be replaced. The CA is what
// keeps trust across the replacement; this is what makes the replacement
// happen. Found by the pre-release review, round ten.
type renewingLeaf struct {
	mu        sync.Mutex
	dir, addr string
	cert      *tls.Certificate
	notAfter  time.Time
	tried     time.Time // the last re-issue attempt, so a failing one is not retried on every handshake
}

// renewRetryEvery bounds how often a re-issue is attempted once the leaf is
// inside its renewal window: a handshake is not the place for a busy loop.
const renewRetryEvery = time.Hour

// tlsConfigFor serves an operator's certificate as it is, and the board's
// own leaf through the renewal above.
func tlsConfigFor(tr transport, dir, addr string, pair tls.Certificate) *tls.Config {
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	if tr.selfIssued {
		cfg.Certificates = nil
		cfg.GetCertificate = newRenewingLeaf(dir, addr, pair).get
	}
	return cfg
}

func newRenewingLeaf(dir, addr string, cert tls.Certificate) *renewingLeaf {
	r := &renewingLeaf{dir: dir, addr: addr, cert: &cert}
	if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
		r.notAfter = leaf.NotAfter
	}
	return r
}

// get is tls.Config.GetCertificate: the current leaf, re-issued first when it
// is inside the renewal window.
func (r *renewingLeaf) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Before(r.notAfter.Add(-certRenewBefore)) || now.Sub(r.tried) < renewRetryEvery {
		return r.cert, nil
	}
	r.tried = now
	certFile, keyFile, err := ensureSelfSignedCert(r.dir, r.addr) // re-issues a leaf the window has reached
	if err != nil {
		slog.Warn("tls: could not renew the board's certificate; serving the current one",
			"err", err, "not_after", r.notAfter.Format(time.RFC3339))
		return r.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		slog.Warn("tls: the renewed certificate and key cannot be used together; serving the current one",
			"err", err)
		return r.cert, nil
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return r.cert, nil
	}
	r.cert, r.notAfter = &pair, leaf.NotAfter
	slog.Info("tls: the board's certificate was renewed", "not_after", leaf.NotAfter.Format(time.RFC3339))
	return r.cert, nil
}
