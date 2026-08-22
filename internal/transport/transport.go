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
	"fmt"
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
// scheme is the transport the operator NAMED, lowercased, or "" for unstated.
// DIBS_ADDR may carry one and every client honours it, so a daemon that read
// the variable and threw the scheme away could serve the opposite of what its
// clients speak: `http://10.0.0.9:4777` served TLS because the address is not
// loopback, and `https://127.0.0.1:4777` served plaintext because it is. Both
// sides read the same variable and disagreed about it. Found by the
// pre-release review.
func Resolve(cfgCert, cfgKey, addr, scheme string, insecurePlaintext bool,
	ensure func() (string, string, error),
) (Choice, error) {
	// A contradiction is refused BEFORE either side of it wins. http:// with a
	// configured certificate is the operator asking for two opposite
	// transports, and quietly honouring the certificate is how a board ends up
	// serving TLS to clients that were told, by the same address, to speak
	// plaintext.
	if scheme == "http" && (cfgCert != "" || cfgKey != "") {
		return Choice{}, fmt.Errorf("the address asks for http:// and a TLS certificate "+
			"is configured: those are opposite transports, and picking one for you "+
			"would leave %s serving something its clients were told not to speak. "+
			"Drop the scheme, or drop tls_cert/tls_key", addr)
	}
	// BOTH halves. A certificate with no key is not something a daemon can
	// serve, and treating it as authoritative pointed clients at a certificate
	// that would never be presented.
	if cfgCert != "" && cfgKey != "" {
		return Choice{cfgCert, cfgKey, "TLS (certificate from config)"}, nil
	}
	// HALF a pair is a mistake, and it used to look like an absence: the daemon
	// started, served plaintext on loopback or an unrelated self-signed
	// certificate off it, and the operator's explicit transport setting did
	// nothing at all. Silence is the worst answer available for a
	// security-sensitive setting that did not take. Raised by the pre-release
	// review, which noted the refactor had blessed the behaviour by moving it
	// here.
	if (cfgCert == "") != (cfgKey == "") {
		missing, given := "tls_key", "tls_cert"
		if cfgCert == "" {
			missing, given = "tls_cert", "tls_key"
		}
		return Choice{}, fmt.Errorf("[%s] is set and [%s] is not: a certificate "+
			"without its key cannot be served, and ignoring half a pair would start "+
			"the daemon on a transport you did not ask for", given, missing)
	}
	// A stated scheme outranks every inference below it, because the operator
	// said it and the clients are going to believe them.
	if c, ok, err := fromStatedScheme(scheme, ensure); ok || err != nil {
		return c, err
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

// fromStatedScheme answers for an address that NAMES its transport. ok is false
// when it names none, which is when the inferences below it apply.
func fromStatedScheme(scheme string, ensure func() (string, string, error)) (Choice, bool, error) {
	switch scheme {
	case "http":
		return Choice{Why: "plaintext (http:// in the address you gave)"}, true, nil
	case "https":
		if ensure == nil {
			return Choice{Why: "TLS (https:// in the address you gave)"}, true, nil
		}
		cert, key, err := ensure()
		if err != nil {
			return Choice{}, true, err
		}
		return Choice{cert, key, "TLS (https:// in the address you gave)"}, true, nil
	}
	return Choice{}, false, nil
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
