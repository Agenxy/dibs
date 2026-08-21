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
	if !strings.Contains(tls, "dibs trust hub.example:4777") {
		t.Errorf("a non-loopback board is configured with no trust step, so the bridge "+
			"will reject it:\n%s", tls)
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

	// The directory in the prose and the directory in the config must be the
	// same one. They were not: the README named ~/.dibs-hub while the command
	// derived ~/.dibs-board-4777, so the secret was copied where nothing read
	// it and the bridge could not start.
	dir := homeDir() + "/.dibs-" + boardSlug("127.0.0.1:4777")
	if strings.Count(loop, dir) < 3 {
		t.Errorf("the data directory is not stated consistently: %q appears %d times in\n%s",
			dir, strings.Count(loop, dir), loop)
	}
}
