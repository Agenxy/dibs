package main

import (
	"os"
	"path/filepath"
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
	if !strings.Contains(tls, "DIBS_DIR="+shellArg(dir)+" dibs trust "+shellArg("hub.example:4777")) {
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
	if !strings.Contains(loop, "ssh -N -L 4777:127.0.0.1:<hub-port>") {
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

// An IPv6 literal is a glob in zsh, so an unquoted address in a pasteable
// command fails with "no matches found" rather than doing anything.
func TestAddressesInShellCommandsAreQuoted(t *testing.T) {
	out, err := captureStdout(t, func() error { return printJoinConfig("[2001:db8::1]:4777") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dibs trust '[2001:db8::1]:4777'") {
		t.Errorf("the trust command's address is unquoted, so zsh globs it:\n%s", out)
	}
	if strings.Contains(out, "dibs trust [2001") {
		t.Error("an unquoted IPv6 address reached a pasteable command")
	}

	// The forward's two ends are independent: the local port is whatever is
	// free here, the far port is whatever the hub listens on. Printing them as
	// the same number brings the tunnel up pointed at nothing, and ssh reports
	// success.
	loop, err := captureStdout(t, func() error { return printJoinConfig("127.0.0.1:5777") })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loop, "5777:127.0.0.1:5777") {
		t.Errorf("the forward assumes the hub uses this machine's port:\n%s", loop)
	}
	if !strings.Contains(loop, "<hub-port>") {
		t.Errorf("the forward does not say the far port is the hub's to name:\n%s", loop)
	}
}

// mcp-config must not print a confident answer to a question nobody asked.
//
// Go stops parsing flags at the first positional, so `mcp-config junk --board
// hub:4777` printed the LOCAL configuration and said nothing about the flag it
// never read: the ignored-argument shape that `configure` was fixed for, one
// command away.
func TestMCPConfigRefusesArgumentsItWouldIgnore(t *testing.T) {
	err := mcpConfig([]string{"junk", "--board", "hub:4777"})
	if err == nil {
		t.Fatal("a positional argument was ignored and the local config printed instead")
	}
	if !strings.Contains(err.Error(), "junk") {
		t.Errorf("the refusal does not name what it refused: %v", err)
	}
}

// The stdio config must name a daemon that is not the default one.
//
// Without DIBS_ADDR and DIBS_DIR it printed a complete-looking config whose
// bridge falls back to 127.0.0.1:4777 and ~/.dibs: an operator running a second
// daemon gets a configuration for the FIRST, reads its secret and its nonce
// file, and joins a board they were not asking about.
func TestConfigNamesANonDefaultDaemon(t *testing.T) {
	// The RENDERED output, not the helpers.
	//
	// The first version of this called nonDefaultEnv and codexEnvLine directly,
	// so it would have passed unchanged if the JSON and TOML blocks stopped
	// using either of them: it proved the helpers work, which was never the
	// thing at risk. Raised by the pre-release review.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_ADDR", "127.0.0.1:4791")
	t.Setenv("DIBS_DIR", dir)
	out, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"127.0.0.1:4791", dir} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed config never names %q, so a second daemon is handed a "+
				"configuration for the first:\n%s", want, out)
		}
	}

	// One env key per TOML table. This was briefly two lines, a protocol
	// version and a daemon address, which is a duplicate-key error in a strict
	// parser and a silent override in a lenient one. Counted in the OUTPUT,
	// including the prose: the note on pinning an identity told the reader to
	// add a second one.
	if n := strings.Count(out, "env = {"); n != 1 {
		t.Errorf("the Codex table gets %d env lines, want 1: a TOML table may not "+
			"repeat a key\n%s", n, out)
	}
	// In the RENDERED env line, not the helper that builds it: the address and
	// directory appear elsewhere in the output too, so checking the whole
	// output would pass even if the Codex block stopped carrying them.
	envLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "env = {") {
			envLine = line
		}
	}
	if envLine == "" {
		t.Fatal("no env line in the rendered Codex block")
	}
	for _, want := range []string{"CODEX_MCP_PROTOCOL_VERSION", "127.0.0.1:4791", dir} {
		if !strings.Contains(envLine, want) {
			t.Errorf("the rendered Codex env line does not carry %q: %s", want, envLine)
		}
	}

	// An explicit scheme is the one thing DIBS_ADDR carries that cannot be
	// inferred: a deliberately plaintext daemon off loopback is reached as
	// http://host:port, and handing the bridge bare host:port makes it infer
	// HTTPS and fail to connect.
	t.Setenv("DIBS_ADDR", "http://hub.example:4777")
	scheme, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	// The BRIDGE's own value. The url block further down carries an origin with
	// a scheme in it regardless, so searching the whole output would pass while
	// the stdio config handed the bridge bare host:port.
	// Every place that hands another process an address, which is both the
	// stdio config and the second-machine recipe printed under it. The url
	// block carries an origin with a scheme regardless, so searching the whole
	// output would pass while either of these handed over bare host:port.
	for _, line := range strings.Split(scheme, "\n") {
		if !strings.Contains(line, "DIBS_ADDR") || !strings.Contains(line, "hub.example") {
			continue
		}
		if !strings.Contains(line, "http://hub.example:4777") {
			t.Errorf("the scheme was stripped from an address handed to a bridge, which "+
				"will then infer HTTPS and fail to connect: %s", line)
		}
	}
	if !strings.Contains(scheme, "http://hub.example:4777") {
		t.Fatal("the address never appeared at all, so this proves nothing")
	}

	// And a plaintext board off loopback must not be told to record a
	// certificate it does not serve.
	joined, err := captureStdout(t, func() error {
		return printJoinConfig("http://hub.example:4777")
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined, "dibs trust") {
		t.Errorf("a board that names plaintext was told to trust a certificate:\n%s", joined)
	}

	// And the default daemon keeps the config it had.
	t.Setenv("DIBS_ADDR", "")
	t.Setenv("DIBS_DIR", "")
	if env := nonDefaultEnv(); len(env) > 0 {
		t.Errorf("the default daemon grew configuration it does not need: %v", env)
	}
}
