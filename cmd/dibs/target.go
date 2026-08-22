package main

// Where the daemon is, and what to speak to it.
//
// Extracted from main.go, which crossed the file-length limit again. These four
// belong together for a better reason than size: they are one question asked in
// two halves, and every time they have drifted apart the CLI has ended up
// talking to a correctly configured board the wrong way. addr() answers WHERE,
// origin() answers HOW, and both defer to the rules the daemon itself uses
// rather than deciding anything on their own.

import (
	"net"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

// defaultAddr is where a daemon nobody configured is listening.
const defaultAddr = "127.0.0.1:4777"

func addr() string {
	// rawAddr, not the environment alone. `dibs configure` writes the operator's
	// answer to dibs.toml and dibd resolves flag -> environment -> file, so an
	// address configured only in the file was invisible here: doctor built its
	// request from 127.0.0.1:4777, reported a perfectly healthy daemon on
	// 192.168.50.10:4777 as unreachable, and then checked the harness
	// configurations against the wrong board. CONFIGURATION.md says doctor
	// reports what is actually in effect; this is what makes that true. Found
	// by the pre-release review.
	a := rawAddr()
	if a == "" {
		return defaultAddr
	}
	// A scheme is accepted here so one variable can carry a whole origin, but
	// callers that want host:port get host:port.
	if _, rest, found := strings.Cut(a, "://"); found {
		return rest
	}
	return a
}

// origin is the daemon's base URL, scheme included.
//
// The scheme is NOT a separate setting, because it is not a free choice: the
// daemon serves plaintext on loopback and TLS on anything else (see
// resolveTransport in cmd/dibd/config.go), so a client that guessed the other
// way simply cannot connect. Deriving it from the same rule means the two
// agree by construction rather than by the operator configuring both to match.
//
// Every request in this binary used to be built as origin(), in
// eighteen places. That is correct for loopback and wrong for every other
// address, so the moment a daemon was moved off 127.0.0.1 to serve a second
// machine, its own CLI could no longer talk to it: `dibs board`, `dibs doctor`
// and the rest failed against a daemon that was working perfectly.
//
// An explicit scheme in DIBS_ADDR wins, for the one case the rule cannot infer:
// a daemon deliberately serving plaintext off-loopback (insecure_plaintext).
// The two schemes, named rather than written inline. A find-and-replace over
// `"http://" + addr()` is exactly how this function's own body was turned into
// a call to itself, which recursed until the stack ran out. Constants are not
// tidiness here; they are what puts this out of a sweep's reach.
const (
	schemePlain = "http://"
	schemeTLS   = "https://"
)

func origin() string {
	if a := rawAddr(); a != "" {
		if scheme, _, found := strings.Cut(a, "://"); found {
			// LOWERCASED. checkAddr accepts a scheme case-insensitively, and
			// this preserved the operator's spelling, so `HTTPS://host:4777`
			// passed validation and then failed inside Go's HTTP transport as
			// an unsupported scheme: a valid address rejected at the last
			// possible moment, by a layer that cannot explain itself. Found by
			// the pre-release review.
			return strings.ToLower(scheme) + "://" + addr()
		}
	}
	// ASK THE SHARED RESOLVER, which is what the daemon serves by.
	//
	// This used to infer from the address alone: loopback means plaintext,
	// anything else means TLS. That ignores two settings the daemon honours and
	// dibs.toml supports, so both of these had every CLI command speaking the
	// wrong transport to a correctly configured daemon:
	//
	//	insecure_plaintext = true on a LAN address  -> daemon plaintext, CLI HTTPS
	//	tls_cert/tls_key on a loopback address      -> daemon TLS, CLI HTTP
	//
	// It reaches doctor, mcp-stdio, admin, await and every ordinary request.
	// Round six fixed which ADDRESS the CLI reads and left it deciding the
	// transport by itself; the pre-release review pointed out the agreement
	// test calls the shared resolver directly and so could not see that this
	// function never did.
	if scheme, _, err := resolveTransport(paths.DataDir()); err == nil && scheme != "" {
		return scheme + "://" + addr()
	}
	// Unreadable or absent config: back to the inference, which is right for
	// the default board and is the only answer available.
	if isLoopbackHostPort(addr()) {
		return schemePlain + addr()
	}
	return schemeTLS + addr()
}

// isLoopbackHostPort mirrors the daemon's own loopback test. A host it cannot
// parse is treated as remote: assuming plaintext for something unrecognised is
// the failure that cannot be undone by a retry.
func isLoopbackHostPort(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
