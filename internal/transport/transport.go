// Package transport holds the ONE rule for what a Dibs daemon serves.
//
// It exists because there were two. The daemon decided from its configuration
// and its address; the CLI, printing the configuration agents connect with,
// decided again from whichever of those it happened to read, and disagreed
// three times in three review rounds: a certificate file left behind made it
// say HTTPS for a loopback daemon, insecure_plaintext was allowed to beat an
// explicit certificate pair the daemon honours first, and a tls_cert with no
// tls_key was treated as enough. Every one of those told an operator to
// configure a client for a transport the daemon does not serve.
//
// A rule that two programs must agree on belongs in neither of them.
package transport

import (
	"net"
	"strings"
)

// Choice is the resolved decision. Empty CertFile means plaintext.
type Choice struct {
	CertFile, KeyFile string
	Why               string // one line, so the choice is never a mystery
}

// TLS reports whether this choice serves TLS.
func (c Choice) TLS() bool { return c.CertFile != "" }

// Resolve picks the secure option for the address without asking the operator.
// The correct configuration is the one you get by doing nothing.
//
//	an explicit certificate PAIR → TLS, using it
//	loopback                     → plaintext (nothing else can reach it)
//	insecure_plaintext           → plaintext (the operator accepted this)
//	anything else                → TLS, with a certificate this machine makes
//
// ensure supplies that last certificate. The daemon generates one; a caller
// that only needs to describe the daemon passes something that reports where it
// would be, and a caller that cannot answer at all passes nil, which yields the
// TLS choice with no paths.
func Resolve(cfgCert, cfgKey, addr string, insecurePlaintext bool,
	ensure func() (string, string, error),
) (Choice, error) {
	// BOTH halves. A certificate with no key is not something a daemon can
	// serve, and treating it as authoritative pointed clients at a certificate
	// that would never be presented.
	if cfgCert != "" && cfgKey != "" {
		return Choice{cfgCert, cfgKey, "TLS (certificate from config)"}, nil
	}
	// Before insecure_plaintext, and before any certificate on disk: a loopback
	// daemon is unreachable from other hosts, so it serves plaintext however
	// many certificates are lying around from when it did not.
	if IsLoopback(addr) {
		return Choice{Why: "plaintext (loopback: unreachable from other hosts)"}, nil
	}
	if insecurePlaintext {
		return Choice{Why: "plaintext (insecure_plaintext set in config: you accepted this)"}, nil
	}
	if ensure == nil {
		return Choice{Why: "TLS (self-signed certificate, auto-generated)"}, nil
	}
	cert, key, err := ensure()
	if err != nil {
		return Choice{}, err
	}
	return Choice{cert, key, "TLS (self-signed certificate, auto-generated)"}, nil
}

// IsLoopback reports whether an address names only this machine.
//
// A wildcard bind is NOT loopback: it binds every interface, so the daemon is
// reachable from other hosts and must serve TLS.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
