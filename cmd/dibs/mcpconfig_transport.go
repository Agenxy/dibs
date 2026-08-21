package main

import (
	"net"
	"path/filepath"
	"strings"
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
	scheme = "http"
	if p := filepath.Join(dir, "tls-cert.pem"); fileExists(p) {
		scheme, certPath = "https", p
	}
	if cfg.TLSCert != "" {
		scheme, certPath = "https", cfg.TLSCert
	}
	if cfg.InsecurePlaintext {
		scheme, certPath = "http", ""
	}
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
func clientHost(a string) string {
	h, p, err := net.SplitHostPort(a)
	if err != nil {
		return a
	}
	switch h {
	case "0.0.0.0", "::", "[::]", "":
		lan, _, lerr := net.SplitHostPort(defaultLANAddr())
		if lerr != nil || lan == "0.0.0.0" {
			return "<this-machine>:" + p
		}
		return net.JoinHostPort(lan, p)
	}
	return a
}

// joinerAddr is the address the OTHER machine reaches this daemon on.
//
// Not this daemon's own address, which is what it printed. For a loopback hub
// that is 127.0.0.1:4777, and the joining machine's 127.0.0.1:4777 is its OWN
// board: the recipe handed over a ready-looking configuration pointed at the
// wrong daemon, and then, further down, correctly explained that the local end
// of a forward is that machine's choice. Two halves of one recipe contradicting
// each other. It names the placeholder the rest of the recipe uses.
func joinerAddr() string {
	if tunnel, _ := boardShape(rawAddr()); tunnel {
		return "127.0.0.1:<local-port>"
	}
	// And a wildcard bind is not dialable from there either: 0.0.0.0 means
	// "every interface" to the daemon and nothing at all to a client.
	raw := rawAddr()
	if scheme, rest, found := strings.Cut(raw, "://"); found {
		return scheme + "://" + clientHost(rest)
	}
	return clientHost(raw)
}
