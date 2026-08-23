package main

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	xport "github.com/agenxy/dibs/internal/transport"
)

// How this daemon is REACHED, as opposed to how it is configured: the transport
// it serves, the address a client can dial, and the address a second machine
// would use. Each is a question with more than one thing holding an opinion,
// and every one of them was answered from the wrong opinion at least once.

// resolveTransport answers what this daemon actually serves, and with which
// certificate, from everything that has an opinion.
//
// In order of authority: an explicit scheme in DIBS_ADDR is the operator
// stating it outright; then dibs.toml, where insecure_plaintext and a
// configured tls_cert are also statements; then the presence of tls-cert.pem in
// the data directory, which is only evidence. Reading the file alone printed an
// https url and instructions to trust a certificate for daemons that serve
// neither, because the file outlives the configuration that made it.
func resolveTransport(dir string) (scheme, certPath string, err error) {
	cfg, err := readBoardConfig(dir)
	if err != nil {
		return "", "", err
	}
	// The daemon's own rule, from the package they share. Answering it here
	// separately is what produced three rounds of the CLI describing a
	// transport the daemon does not serve.
	//
	// ensure reports where the daemon WOULD have put a self-signed certificate,
	// without making one: this is a description of that daemon, not a second
	// program deciding things for it.
	choice, err := xport.Resolve(cfg.TLSCert, cfg.TLSKey, hostPort(rawAddr()),
		statedScheme(rawAddr()), cfg.InsecurePlaintext,
		func() (string, string, error) {
			return filepath.Join(dir, "tls-cert.pem"), filepath.Join(dir, "tls-key.pem"), nil
		})
	if err != nil {
		return "", "", err
	}
	scheme = "http"
	if choice.TLS() {
		scheme = "https"
		certPath = choice.CertFile
		// Only a certificate that is actually there can be handed to anybody.
		// The daemon makes it on first start; before that there is nothing to
		// point at, and naming a path that does not exist is worse than saying
		// nothing.
		if !fileExists(certPath) {
			certPath = ""
		}
	}
	// An explicit scheme in DIBS_ADDR is the operator stating it outright, and
	// outranks everything above.
	if given, _, found := strings.Cut(rawAddr(), "://"); found {
		scheme = strings.ToLower(given)
		if scheme != "https" {
			certPath = ""
		}
	}
	return scheme, certPath, nil
}

// clientHost turns a listen address into one a client can dial.
//
// A wildcard bind is the case that matters: 0.0.0.0 and :: mean "every
// interface", and printing either in a url hands somebody a string that cannot
// connect from anywhere. The machine's own LAN address is what they meant, and
// is what the wizard offers for the same choice.
func clientHost(a string) (string, error) {
	h, p, err := net.SplitHostPort(a)
	if err != nil {
		// Not host:port at all: a bare hostname or something already shaped for
		// a client. Nothing to substitute, so hand it back unchanged rather than
		// inventing a failure the caller cannot act on. Shape is checked by
		// checkAddrShape, which is a different question from dialability.
		return a, nil //nolint:nilerr // see above: not a failure, nothing to do
	}
	switch h {
	case "0.0.0.0", "::", "[::]", "":
		lan, _, lerr := net.SplitHostPort(defaultLANAddr())
		if lerr != nil || lan == "0.0.0.0" {
			// A PLACEHOLDER is not an address, and printing one inside an
			// otherwise complete configuration is the /home/you mistake again:
			// the command exits 0 and hands somebody a string that cannot
			// connect from anywhere, with nothing saying it is a stand-in.
			return "", fmt.Errorf("this daemon listens on %s, a wildcard, and no "+
				"address of this machine could be detected to put in its place. A "+
				"client cannot dial a wildcard: set `addr` in dibs.toml to the address "+
				"agents should reach this board on", a)
		}
		return net.JoinHostPort(lan, p), nil
	}
	return a, nil
}

// schemePrefix turns a resolved scheme into what goes in front of an address,
// and "" into "" so a caller with nothing to say says nothing.
func schemePrefix(served string) string {
	if served == "" {
		return ""
	}
	return served + "://"
}

// joinerAddr is the address the OTHER machine reaches this daemon on.
//
// Not this daemon's own address, which is what it printed. For a loopback hub
// that is 127.0.0.1:4777, and the joining machine's 127.0.0.1:4777 is its OWN
// board: the recipe handed over a ready-looking configuration pointed at the
// wrong daemon, and then, further down, correctly explained that the local end
// of a forward is that machine's choice. Two halves of one recipe contradicting
// each other. It names the placeholder the rest of the recipe uses.
// served is the transport this daemon actually resolved. It is STATED in the
// address handed to the other machine, always, rather than left for the
// joining bridge to infer from the host: a bare LAN address with
// `insecure_plaintext = true` was emitted without a scheme and the bridge
// re-inferred HTTPS, and a bare loopback address with a certificate pair was
// emitted without one and the bridge re-inferred HTTP. Both configurations are
// supported and both produced a recipe that cannot connect. Found by the
// pre-release review.
func joinerAddr(served string) (string, error) {
	if tunnel, _ := boardShape(rawAddr(), served); tunnel {
		// STATED HERE TOO. This branch returned the bare placeholder and threw
		// `served` away, which is the same defect the rest of this function was
		// written to fix, surviving in the one case the comment above names
		// explicitly: a loopback daemon with a certificate pair. The forward
		// carries TLS to the far end, the joining bridge saw a bare loopback
		// address and inferred plain HTTP, and the recipe printed a trust step
		// for a certificate it would then never present. Successful exit,
		// unusable configuration.
		return schemePrefix(served) + "127.0.0.1:<local-port>", nil
	}
	raw := rawAddr()
	rest := raw
	if scheme, r, found := strings.Cut(raw, "://"); found {
		rest = r
		if served == "" {
			served = strings.ToLower(scheme)
		}
	}
	h, err := clientHost(rest)
	if err != nil {
		return "", err
	}
	if served == "" {
		return h, nil
	}
	return served + "://" + h, nil
}

// statedScheme returns the transport an address NAMES, lowercased, or "" when
// it names none. It is deliberately not inferredScheme: that one guesses for an
// address that stayed quiet, and this one reports only what was actually said,
// because the daemon is given the same string and must reach the same answer.
func statedScheme(a string) string {
	scheme, _, found := strings.Cut(a, "://")
	if !found {
		return ""
	}
	switch s := strings.ToLower(scheme); s {
	case "http", "https":
		return s
	}
	return ""
}
