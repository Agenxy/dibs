package main

import (
	"strings"
	"testing"
)

// Every board gets its own credential directory, keyed by the WHOLE address.
//
// The port was kept only for loopback, on the reasoning that a forwarded port
// is the only thing telling two tunnels apart. True, and not the whole set:
// two daemons on one host is a configuration Dibs supports deliberately, and
// both resolved to one directory. Copying the second board's secret would
// overwrite the first board's while each generated config claimed the shared
// directory was its own.
func TestEachBoardGetsItsOwnDirectory(t *testing.T) {
	seen := map[string]string{}
	for _, addr := range []string{
		"127.0.0.1:4777", "127.0.0.1:5777",
		"hub.example:4777", "hub.example:5777",
		"other.example:4777",
		// Two different hosts that a cosmetic dot-to-hyphen rewrite mapped
		// onto one directory, which is the port collision again in another
		// character.
		"hub-example:4777",
		// And the same shape a third time: an IPv6 literal and a hostname
		// spelled like one, which the colon rewrite maps together. Contrived,
		// and the comment on boardSlug claims EVERY address gets its own
		// directory, so it has to hold rather than hold for the examples
		// somebody thought of.
		"[2001:db8::1]:4777",
		"2001-db8--1:4777",
	} {
		slug := boardSlug(addr)
		if prev, clash := seen[slug]; clash {
			t.Errorf("%s and %s share the directory %q: joining the second overwrites "+
				"the first board's secret", prev, addr, slug)
		}
		seen[slug] = addr
	}
}

// The bridge trusts only what the joining machine has recorded, so a board that
// is not on loopback needs `dibs trust` before any of this works.
//
// Without it the printed configuration is complete-looking and the bridge
// rejects the board on its first call. The older TLS recipe said so; this
// command was written without it.
func TestJoinConfigNamesTheStepTheAddressCallsFor(t *testing.T) {
	tls, err := captureStdout(t, func() error { return printJoinConfig("hub.example:4777") })
	if err != nil {
		t.Fatal(err)
	}
	// The trust command must carry the board's DIBS_DIR.
	//
	// `dibs trust` records the certificate in the data directory it is given,
	// and the bridge reads it from the one in the config. Printed bare, trust
	// writes into ~/.dibs, reports success, and the bridge still rejects the
	// board: a step that looks done and is not.
	dir := homeDir() + "/.dibs-" + boardSlug("hub.example:4777")
	if !strings.Contains(tls, "DIBS_DIR="+dir+" dibs trust hub.example:4777") {
		t.Errorf("the trust step does not name the board's data directory, so the "+
			"certificate lands where the bridge never looks:\n%s", tls)
	}
	if strings.Contains(tls, "ssh -N -L") {
		t.Error("a directly reachable board was told to open an ssh forward")
	}

	loop, err := captureStdout(t, func() error { return printJoinConfig("127.0.0.1:4777") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loop, "ssh -N -L 4777:127.0.0.1:4777") {
		t.Errorf("a loopback address is the near end of a forward and nothing said how "+
			"to open it:\n%s", loop)
	}
	if strings.Contains(loop, "dibs trust") {
		t.Error("a loopback board was told to record a certificate it does not serve")
	}

	// The hub's own data directory cannot be inferred from here: a hub serving
	// from DIBS_DIR=~/.dibs-team has no local.secret at ~/.dibs, and a hub that
	// ALSO runs a default board has the wrong one there. Hard-coding the path
	// copies a credential for a board nobody meant.
	for _, out := range []string{tls, loop} {
		if strings.Contains(out, "<hub>:~/.dibs/local.secret") {
			t.Errorf("the recipe hard-codes the hub's data directory, which only the hub "+
				"knows:\n%s", out)
		}
	}

	// The directory in the prose and the directory in the config must be the
	// same one. They were not: the README named ~/.dibs-hub while the command
	// derived ~/.dibs-board-4777, so the secret was copied where nothing read
	// it and the bridge could not start.
	loopDir := homeDir() + "/.dibs-" + boardSlug("127.0.0.1:4777")
	if strings.Count(loop, loopDir) < 3 {
		t.Errorf("the data directory is not stated consistently: %q appears %d times in\n%s",
			loopDir, strings.Count(loop, loopDir), loop)
	}
}
