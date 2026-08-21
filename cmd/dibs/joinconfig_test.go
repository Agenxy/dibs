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
		// And a fourth: loopback was renamed "board", which is an ordinary
		// hostname somebody may well be using. Each of these was one character
		// kept after the last; the address is kept verbatim now.
		"board:4777",
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
	if !strings.Contains(tls, "DIBS_DIR="+shellArg(dir)+" dibs trust hub.example:4777") {
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
	if strings.Count(loop, loopDir) < 2 {
		t.Errorf("the data directory is not stated consistently: %q appears %d times in\n%s",
			loopDir, strings.Count(loop, loopDir), loop)
	}
}

// The generated shell lines are pasted, so a home directory with a space in it
// must not split into two arguments. shellArg exists for this and was not used.
func TestGeneratedShellLinesAreQuoted(t *testing.T) {
	t.Setenv("HOME", "/tmp/home with space")
	out, err := captureStdout(t, func() error { return printJoinConfig("hub.example:4777") })
	if err != nil {
		t.Fatal(err)
	}
	// Only the SHELL lines. The JSON and TOML blocks carry the path as a value
	// in their own syntax, already quoted, and are not pasted into a shell.
	shellVerbs := []string{"mkdir -p", "scp ", "dibs trust"}
	checked := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "home with space") {
			continue
		}
		isShell := false
		for _, v := range shellVerbs {
			if strings.Contains(line, v) {
				isShell = true
			}
		}
		if !isShell {
			continue
		}
		checked++
		if !strings.Contains(line, "'/tmp/home with space") {
			t.Errorf("unquoted path in a pasteable command, which would split into two "+
				"arguments: %q", line)
		}
	}
	if checked == 0 {
		t.Fatal("no shell line carried the home directory, so this proves nothing")
	}

	// The scp SOURCE is a placeholder the operator replaces with the hub's own
	// data directory, which may equally contain a space. Quoting only the half
	// this machine controls leaves the other half to split.
	if !strings.Contains(out, "'<hub>:<hub-data-dir>/local.secret'") {
		t.Error("the scp source is unquoted, so a hub data directory with a space in " +
			"it splits into two arguments when the operator substitutes it")
	}
}

// The recovery message must carry the data directory the failing call used.
//
// An operator on a joined board reads "dibs trust <addr>", pastes it without
// the variable, and records the certificate in the DEFAULT data directory:
// trust reports success and the bridge goes on refusing the board, which is
// the failure this message exists to end. Found by the pre-release review as
// the third place the same fix was missing.
func TestTheTrustAdviceCarriesTheDataDirectory(t *testing.T) {
	t.Setenv("DIBS_DIR", "/tmp/board with space")
	got := trustCommand()
	if !strings.HasPrefix(got, "DIBS_DIR='/tmp/board with space' dibs trust ") {
		t.Errorf("trust advice does not carry (or does not quote) DIBS_DIR: %q", got)
	}

	t.Setenv("DIBS_DIR", "")
	if bare := trustCommand(); strings.Contains(bare, "DIBS_DIR") {
		t.Errorf("the single-board case grew a variable with nothing to carry: %q", bare)
	}
}
