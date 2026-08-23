package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	home, herr := homeDir()
	if herr != nil {
		t.Fatal(herr)
	}
	dir := home + "/.dibs-" + boardSlug("hub.example:4777")
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
	loopDir := home + "/.dibs-" + boardSlug("127.0.0.1:4777")
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
	// COUNTED, because a loop over lines that match nothing asserts nothing.
	// Removing the address from both bridge blocks made this perform zero
	// checks and pass, while the whole-output check below still succeeded on
	// the url block's own origin.
	handed := 0
	for _, line := range strings.Split(scheme, "\n") {
		if !strings.Contains(line, "DIBS_ADDR") || !strings.Contains(line, "hub.example") {
			continue
		}
		handed++
		if !strings.Contains(line, "http://hub.example:4777") {
			t.Errorf("the scheme was stripped from an address handed to a bridge, which "+
				"will then infer HTTPS and fail to connect: %s", line)
		}
	}
	// TWO bridge blocks hand over an address here: the stdio config and the
	// second-machine recipe under it. Anything less means the loop above
	// skipped one of them, and a skipped line is a line that cannot fail.
	if handed < 2 {
		t.Fatalf("only %d line(s) handed an address to a bridge; the stdio config "+
			"and the remote recipe each do, so the loop above inspected less than it "+
			"claims and would pass with the address removed from one of them", handed)
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
	if env := nonDefaultEnv("http"); len(env) > 0 {
		t.Errorf("the default daemon grew configuration it does not need: %v", env)
	}
}

// `dibs trust` dials host:port, so a scheme reaches tls.Dial as part of the
// host and fails with "too many colons in address". The scheme belongs in
// DIBS_ADDR, which is a different argument to a different program: round eight
// preserved it everywhere and this is the one place it must not go.
func TestTrustIsGivenHostPortAndNotAScheme(t *testing.T) {
	out, err := captureStdout(t, func() error { return printJoinConfig("https://hub.example:4777") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dibs trust 'hub.example:4777'") {
		t.Errorf("the trust command does not get a bare host:port:\n%s", out)
	}
	if strings.Contains(out, "dibs trust 'https://") {
		t.Error("a scheme reached `dibs trust`, which cannot dial it")
	}

	// And the credential directory names the BOARD, not the way it is reached:
	// http://hub and https://hub are one daemon on one address.
	if a, b := boardSlug("https://hub.example:4777"), boardSlug("hub.example:4777"); a != b {
		t.Errorf("one board got two credential directories, %q and %q, for the same "+
			"address written two ways", a, b)
	}
}

// The headless invocation must reach the command, AND DO SOMETHING.
//
// `dibs mcp-config --board <addr>` is documented for a second machine, which is
// typically headless and driven by `ssh host command`. adminOnly gated the
// whole verb before --board was parsed, so that invocation printed "needs an
// interactive terminal" and nothing else: the exact machine the command was
// added for. The tests called mcpConfig directly and never reached the gate,
// which is why it took the reviewer running the shipped CLI to find it.
func TestJoiningAnotherBoardIsNotGatedOnATerminal(t *testing.T) {
	// Through the GATE, not the predicate.
	//
	// The first version called joiningAnotherBoard only, so restoring the
	// dispatch to gate the whole verb, which is the regression this names,
	// would have left it green. go test gives this process a pipe for stdout,
	// so adminOnly sees no terminal: exactly the headless case.
	// AND IT HAS TO PRODUCE THE RECIPE. Only "not the terminal error" was
	// asserted, so any OTHER failure satisfied it, and so did a path that
	// reached the command and did nothing at all: the check could not tell
	// "ungated and working" from "ungated and broken in a new way".
	out, err := captureStdout(t, func() error {
		return mcpConfigEntry([]string{"--board", "hub.example:4777"})
	})
	if err != nil {
		if strings.Contains(err.Error(), "interactive terminal") {
			t.Fatalf("joining another board was refused for want of a terminal, which "+
				"is the machine it exists for: %v", err)
		}
		t.Fatalf("joining another board failed: %v", err)
	}
	if !strings.Contains(out, "hub.example:4777") {
		t.Errorf("the headless invocation printed no configuration for the board it "+
			"was given, so it is ungated and does nothing:\n%s", out)
	}
	if err := mcpConfigEntry(nil); err == nil ||
		!strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("the plain form was not gated, and it prints this machine's secret: %v", err)
	}

	for _, args := range [][]string{
		{"--board", "hub.example:4777"},
		{"--board=hub.example:4777"},
	} {
		if !joiningAnotherBoard(args) {
			t.Errorf("%v is not recognised as joining another board, so it would be "+
				"refused on a headless machine", args)
		}
	}
	// The plain form prints THIS daemon's local secret, so it stays gated.
	if joiningAnotherBoard([]string{}) || joiningAnotherBoard([]string{"--help"}) {
		t.Error("the plain form was let past the gate that exists because it prints " +
			"this machine's secret")
	}
}

// A board can need BOTH a forward and a certificate recorded.
//
// The branch was a switch, which reads as "one of these" and dropped a real
// combination: `https://127.0.0.1:5777` is a forwarded HTTPS board, and the
// tunnel arm won, so the printed configuration was complete-looking and
// rejected the certificate. boardShape had both answers right; the branch threw
// one away. Found by the pre-release review running the shipped binary.
func TestABoardCanNeedAForwardAndACertificate(t *testing.T) {
	out, err := captureStdout(t, func() error { return printJoinConfig("https://127.0.0.1:5777") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ssh -N -L") {
		t.Errorf("a loopback address was given no forward:\n%s", out)
	}
	if !strings.Contains(out, "dibs trust") {
		t.Errorf("an https board was given no trust step, so the bridge will reject "+
			"its certificate:\n%s", out)
	}
	// Two steps that both print must not both be called 2.
	if strings.Count(out, "\n# 2. ") > 1 {
		t.Errorf("two steps share a number:\n%s", out)
	}

	// A scheme is not case-sensitive, and reading HTTPS:// as plaintext would
	// skip the trust step on a board that does serve a certificate.
	upper, err := captureStdout(t, func() error { return printJoinConfig("HTTPS://hub.example:4777") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upper, "dibs trust") {
		t.Errorf("an uppercase scheme was read as plaintext:\n%s", upper)
	}
}

// An empty --board must not be read as a request for the LOCAL configuration.
//
// It was: `--board=` passed the scan that waives the interactive gate, then
// parsed as empty and fell through to the local form, which prints this
// daemon's secret. So `dibs mcp-config --board=` over ssh returned the secret
// and exited 0. The waiver was added one round earlier and has to be as narrow
// as the thing it waives for. Found by the pre-release review running the
// binary.
func TestAnEmptyBoardIsNeitherWaivedNorTreatedAsLocal(t *testing.T) {
	for _, args := range [][]string{
		{"--board="},
		{"--board"},
		{"--board", "--help"},
	} {
		if joiningAnotherBoard(args) {
			t.Errorf("%v waived the gate that exists because the local form prints this "+
				"machine's secret", args)
		}
	}

	// And it is refused rather than silently doing something else.
	if err := mcpConfig([]string{"--board="}); err == nil {
		t.Error("an empty --board printed the local configuration instead of being refused")
	}

	// A real address still waives it: that is the headless case the flag exists for.
	if !joiningAnotherBoard([]string{"--board", "hub.example:4777"}) {
		t.Error("a real board no longer waives the gate, so the headless case is refused again")
	}
}

// The ORDINARY recipe reads the address too, not a boolean about a file.
//
// It took "did the daemon make a certificate", which answers neither question:
// an https board on loopback needs a forward AND a trust step and got only the
// forward, and a board explicitly named http:// off loopback was called
// loopback and told to tunnel. Found by the pre-release review running the
// binary; `--board` had been fixed and this had not.
func TestTheOrdinaryRecipeReadsTheAddress(t *testing.T) {
	// EACH CASE CARRIES THE TRANSPORT ITS DAEMON ACTUALLY SERVES.
	//
	// This passed servesTLS=true for every row while expecting a bare loopback
	// address to omit the trust step, so it pinned the answer for a daemon that
	// does not exist: TLS on loopback needs a trust step and was asserted not to
	// print one. The two config-derived rows at the end are the ones the
	// pre-release review found broken, and neither could be expressed before,
	// because the recipe read only the address.
	cases := []struct {
		name                  string
		addr                  string
		servesTLS             bool
		wantTunnel, wantTrust bool
	}{
		{
			"https on loopback: forward it AND trust the certificate",
			"https://127.0.0.1:5777", true, true, true,
		},
		{
			"http off loopback: neither",
			"http://hub.example:4777", false, false, false,
		},
		{
			"the default loopback daemon: forward, nothing to trust",
			"127.0.0.1:4777", false, true, false,
		},
		{
			"a LAN daemon on the default transport: TLS, so trust it",
			"hub.example:4777", true, false, true,
		},
		{
			"a bare loopback address whose daemon was given a certificate",
			"127.0.0.1:4777", true, true, true,
		},
		{
			"a bare LAN address with insecure_plaintext",
			"hub.example:4777", false, false, false,
		},
	}
	for _, c := range cases {
		t.Setenv("DIBS_ADDR", c.addr)
		out, err := captureStdout(t, func() error {
			printRemoteRecipe(c.servesTLS, mustJoinerFor(t, c.servesTLS))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "ssh -N -L"); got != c.wantTunnel {
			t.Errorf("%s (%s): forward printed = %v, want %v", c.name, c.addr, got, c.wantTunnel)
		}
		if got := strings.Contains(out, "dibs trust"); got != c.wantTrust {
			t.Errorf("%s (%s): trust step printed = %v, want %v. A daemon serving TLS "+
				"needs its certificate recorded, and one serving plaintext has nothing "+
				"to record: the ADDRESS cannot answer that on its own",
				c.name, c.addr, got, c.wantTrust)
		}
	}
}

// The wizard writes an address; the command it sends you to must read it.
//
// `dibs configure` asks where agents connect from, writes the answer to
// dibs.toml, and ends by saying to run `dibs mcp-config`. That printed a
// configuration for 127.0.0.1:4777 regardless, so an operator who chose LAN or
// internet got a confident answer about the wrong daemon from the command the
// wizard had just sent them to. Found by the pre-release review.
func TestTheConfigFollowsTheWizardsChoice(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"),
		[]byte("addr = \"0.0.0.0:4777\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	out, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "127.0.0.1:4777") {
		t.Errorf("the configured address was ignored in favour of the default:\n%s", out)
	}

	// A wildcard bind is a LISTEN address: 0.0.0.0 means every interface to the
	// daemon and nothing anybody can connect to. Printing it in a url or in
	// another machine's DIBS_ADDR hands over a string that cannot work.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"url"`) || strings.Contains(line, `"DIBS_ADDR"`) {
			if strings.Contains(line, "0.0.0.0") {
				t.Errorf("a wildcard bind reached something a client has to dial: %s", line)
			}
		}
	}
}

// A certificate FILE is not proof the daemon serves TLS today.
//
// It outlives the configuration that made it: a daemon moved back to loopback,
// or switched to insecure_plaintext, keeps its tls-cert.pem. Reading the file
// alone printed an http:// url under the heading "this daemon serves HTTPS",
// with instructions to trust a certificate it is not presenting.
func TestAStaleCertificateDoesNotClaimTLS(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"local.secret": strings.Repeat("a", 64),
		"tls-cert.pem": "-- not a real certificate --",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "http://hub.example:4777")

	out, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "serves HTTPS") {
		t.Errorf("an address explicitly naming plaintext was declared HTTPS because a "+
			"certificate file was left behind:\n%s", out)
	}
	if strings.Contains(out, "https://hub.example") {
		t.Error("the url contradicts the scheme the operator wrote")
	}
}

// The recipe must not hand the joining machine THIS daemon's loopback address.
//
// For a loopback hub it printed 127.0.0.1:4777 as that machine's DIBS_ADDR and
// in the --board command it suggests. On that machine 127.0.0.1:4777 is its
// OWN board: a ready-looking configuration pointed at the wrong daemon, in a
// recipe that goes on to explain, correctly, that the local end of a forward is
// that machine's choice. Two halves of one output contradicting each other.
func TestTheRecipeNamesTheOtherMachinesAddress(t *testing.T) {
	t.Setenv("DIBS_ADDR", "127.0.0.1:4777")
	out, err := captureStdout(t, func() error { printRemoteRecipe(false, mustJoiner(t)); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "DIBS_ADDR") && !strings.Contains(line, "--board") {
			continue
		}
		if strings.Contains(line, "127.0.0.1:4777") {
			t.Errorf("the joining machine is told to use this daemon's own loopback "+
				"address, which on that machine is its own board: %s", line)
		}
	}
	if !strings.Contains(out, "<local-port>") {
		t.Errorf("the recipe never says the local end is that machine's choice:\n%s", out)
	}
}

// EVERY block that configures a bridge must carry the configured address.
//
// nonDefaultEnv read addr(), which ignores dibs.toml, so a daemon configured
// onto a LAN address or a non-default port had its stdio configs printed with
// no DIBS_ADDR at all and the bridge dialled 127.0.0.1:4777. The url block had
// already been fixed and this had not: the wildcard fixture used elsewhere
// misses it, because a wildcard bind is still reachable over loopback.
func TestEveryBridgeBlockCarriesTheConfiguredAddress(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"),
		[]byte("addr = \"192.168.50.10:4777\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	out, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	// The JSON block, the TOML block, and the url: three places, one address.
	for _, want := range []string{
		`"DIBS_ADDR": "192.168.50.10:4777"`,
		`DIBS_ADDR = "192.168.50.10:4777"`,
		// https, because that is what the daemon serves on an address off
		// loopback with nothing said. This asserted http when the CLI inferred
		// transport from whether a certificate file happened to exist.
		`"url": "https://192.168.50.10:4777/mcp"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a bridge is configured without the address the daemon is on, so it "+
				"will dial the default and fail. missing: %s\n%s", want, out)
		}
	}
}

// An address that cannot work must be refused, not wrapped in a complete-looking
// configuration.
//
// The shipped command accepted `htps://hub:4777`, called it plaintext because
// the scheme was not "https", and emitted the typo as DIBS_ADDR; and accepted
// `0.0.0.0:4777`, a listen address no client can dial. Both exited 0.
func TestABoardAddressThatCannotWorkIsRefused(t *testing.T) {
	for _, bad := range []string{
		"htps://hub.example:4777",
		"0.0.0.0:4777",
		"[::]:4777",
		"hub.example",
		"ftp://hub.example:4777",
		// MALFORMED HOSTS. The check listed forbidden characters, so it caught
		// the ones somebody thought of and accepted spaces, control characters
		// and invalid escapes. `dibs mcp-config --board 'bad host:4777'` exited
		// zero and printed a confident configuration the bridge could not turn
		// into a request. This list claimed to cover unusable addresses and had
		// no case like it. Found by the pre-release review.
		"bad host:4777",
		"hub\texample:4777",
		"hub%zz.example:4777",
		"hub\x00example:4777",
	} {
		if err := checkBoardAddr(bad); err == nil {
			t.Errorf("%q was accepted, so a configuration will be printed around an "+
				"address that cannot connect", bad)
		}
	}
	for _, good := range []string{
		"127.0.0.1:4777", "hub.example:4777",
		"https://hub.example:4777", "http://hub.example:4777", "[2001:db8::1]:4777",
	} {
		if err := checkBoardAddr(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
}

// A dibs.toml the daemon would refuse is not "no configuration".
//
// It was read as absent, so the daemon would not start while this printed a
// confident configuration for 127.0.0.1:4777. The operator then looks at the
// config the CLI gave them and wonders why nothing connects, with the actual
// fault in a file neither of them mentioned.
func TestAMalformedConfigIsNotSilentlyIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"),
		[]byte("addr = \"unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	_, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err == nil {
		t.Fatal("a config the daemon cannot parse was ignored, and a configuration " +
			"printed for the default address instead")
	}
	if !strings.Contains(err.Error(), "dibs.toml") {
		t.Errorf("the refusal does not name the file at fault: %v", err)
	}

	// No config file at all is a different thing and must still work.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", clean)
	if _, err := captureStdout(t, func() error { return mcpConfig(nil) }); err != nil {
		t.Errorf("a data directory with no dibs.toml was refused: %v", err)
	}
}

// A --board address that cannot name a directory safely must be refused.
//
// The port half goes into the credential directory's name, and a path there
// walked the directory out of the operator's home: `hub:4777/../../escaped`
// produced `mkdir -p /Users/escaped` with a secret written into it. The shipped
// binary accepted it and exited 0. Found by the pre-release review.
func TestABoardAddressCannotSteerTheCredentialDirectory(t *testing.T) {
	for _, bad := range []string{
		"hub.example:4777/../../escaped",
		"hub.example:not-a-port",
		"hub.example:0",
		"hub.example:99999",
	} {
		if err := checkBoardAddr(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// And the slug is safe whatever it is handed, since checkBoardAddr is one
	// caller rather than a property of the function.
	for _, nasty := range []string{"../../escape:4777", "a/b:4777", `a\b:4777`} {
		if slug := boardSlug(nasty); strings.ContainsAny(slug, `/\`) || strings.Contains(slug, "..") {
			t.Errorf("boardSlug(%q) = %q, which is a path", nasty, slug)
		}
	}
}

// The configured transport outranks whatever the data directory happens to
// contain.
//
// `insecure_plaintext = true` beside a left-behind tls-cert.pem had this
// printing an https url and instructions to record a certificate the daemon is
// not presenting.
func TestConfiguredPlaintextOutranksALeftBehindCertificate(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"local.secret": strings.Repeat("a", 64),
		"tls-cert.pem": "-- stale --",
		"dibs.toml":    "addr = \"192.168.50.10:4777\"\ninsecure_plaintext = true\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	out, err := captureStdout(t, func() error { return mcpConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "serves HTTPS") || strings.Contains(out, "https://") {
		t.Errorf("a daemon configured for plaintext was described as serving HTTPS "+
			"because a certificate file was left in its directory:\n%s", out)
	}
}

// A key dibd does not know means dibd will not start, so a configuration
// printed from that file is a guess.
//
// `adrr = "192.168.1.5:4777"` parses as valid TOML. The daemon refuses it; this
// printed a confident configuration for 127.0.0.1:4777.
func TestAConfigTheDaemonWouldRefuseIsRefusedHere(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"local.secret": strings.Repeat("a", 64),
		"dibs.toml":    "adrr = \"192.168.50.10:4777\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	if _, err := captureStdout(t, func() error { return mcpConfig(nil) }); err == nil {
		t.Fatal("a config the daemon refuses was accepted, and a configuration printed " +
			"for the default address")
	} else if !strings.Contains(err.Error(), "adrr") {
		t.Errorf("the refusal does not name the key at fault: %v", err)
	}

	// A nested key under a table dibd knows is that table's business.
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"),
		[]byte("[match]\njoin_threshold = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return mcpConfig(nil) }); err != nil {
		t.Errorf("a valid nested setting was refused: %v", err)
	}
}

// The CLI must describe the transport the DAEMON serves, not a second opinion.
//
// It reached a different answer three rounds running: a leftover certificate
// made it say HTTPS for a loopback daemon; insecure_plaintext was allowed to
// beat a certificate pair the daemon honours first; and a tls_cert with no
// tls_key was treated as authoritative. Both now call internal/transport.
func TestTheCLIAgreesWithTheDaemonAboutTransport(t *testing.T) {
	certPath, keyPath := realPair(t)
	cases := []struct {
		name, toml, addr string
		staleCert        bool
		wantScheme       string
	}{
		{"loopback with a leftover certificate", "", "127.0.0.1:4999", true, "http"},
		{
			"a certificate pair beats insecure_plaintext",
			"addr = \"192.168.50.10:4777\"\ninsecure_plaintext = true\ntls_cert = \"" + certPath +
				"\"\ntls_key = \"" + keyPath + "\"\n", "", false, "https",
		},
		{
			"insecure_plaintext off loopback",
			"addr = \"192.168.50.10:4777\"\ninsecure_plaintext = true\n", "", true, "http",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "local.secret"),
				[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.toml != "" {
				if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(c.toml), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if c.staleCert {
				if err := os.WriteFile(filepath.Join(dir, "tls-cert.pem"), []byte("stale"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DIBS_DIR", dir)
			t.Setenv("DIBS_ADDR", c.addr)

			scheme, _, err := resolveTransport(dir)
			if err != nil {
				t.Fatal(err)
			}
			// AND THE CLI'S OWN ANSWER, which is what the name of this test
			// promises. Calling the shared resolver alone proved that the
			// resolver is right, which it was: this passed for months while
			// origin() inferred the scheme from the address by itself and every
			// CLI command talked to a correctly configured board the wrong way.
			// A test named for an agreement has to ask both sides.
			if got := origin(); !strings.HasPrefix(got, c.wantScheme+"://") {
				t.Errorf("the daemon resolves %q and the CLI dials %q. The resolver "+
					"being right is not the property this names: what matters is that "+
					"the caller asks it", c.wantScheme, got)
			}
			if scheme != c.wantScheme {
				t.Errorf("scheme = %q, want %q", scheme, c.wantScheme)
			}
		})
	}
}

// An unknown key inside a known table is still a key the daemon refuses.
//
// Checking only the table left `[match] typo_threshold = 0.9` fine here while
// dibd exits 1 on it: the round-fifteen failure preserved for every table.
func TestAnUnknownNestedKeyIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	write("[match]\ntypo_threshold = 0.9\n")
	if _, err := readBoardConfig(dir); err == nil {
		t.Error("an unknown key inside [match] was accepted, so a configuration would " +
			"be printed for a daemon that refuses to start")
	}
	write("[match]\njoin_threshold = 0\n")
	if _, err := readBoardConfig(dir); err != nil {
		t.Errorf("a real [match] setting was refused: %v", err)
	}
}

// A home directory that cannot be resolved is refused, not invented.
//
// It answered "/home/you", so a headless host got a complete recipe of mkdir,
// scp, trust and JSON rooted at a literal path that is nobody's home and may be
// somebody else's directory, with nothing saying it was a stand-in.
func TestAnUnresolvableHomeIsRefusedNotInvented(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := homeDir(); err == nil {
		t.Skip("this platform resolves a home without HOME, so there is nothing to probe")
	}
	err := printJoinConfig("hub.example:4777")
	if err == nil {
		t.Fatal("a configuration was printed rooted at an invented home directory")
	}
	if strings.Contains(err.Error(), "/home/you") {
		t.Errorf("the refusal still names the placeholder as if it were real: %v", err)
	}
}

// mustJoinerFor resolves the joining address for a daemon serving a KNOWN
// transport, which is what mcp-config has when it prints the recipe.
func mustJoinerFor(t *testing.T, servesTLS bool) string {
	t.Helper()
	j, err := joinerAddr(servedScheme(servesTLS))
	if err != nil {
		t.Fatalf("resolving the joining address: %v", err)
	}
	return j
}

// mustJoiner resolves the address another machine reaches this daemon on, the
// way mcp-config does before printing anything.
func mustJoiner(t *testing.T) string {
	t.Helper()
	// "" is "the caller does not know the transport", which is the shape the
	// callers that only have an address still use.
	j, err := joinerAddr("")
	if err != nil {
		t.Fatalf("resolving the joining address: %v", err)
	}
	return j
}

// The CLI must speak the transport the daemon SERVES, not the one the address
// suggests.
//
// origin() inferred from the address alone: loopback means plaintext, anything
// else means TLS. dibs.toml supports two settings that change the answer and
// the daemon honours both through the shared resolver, so a correctly
// configured board had every CLI command talking to it the wrong way: doctor,
// mcp-stdio, admin, await and the ordinary request paths.
//
// Round six made the CLI read the right ADDRESS and left it deciding the
// transport by itself. The pre-release review pointed out that the agreement
// test calls the shared resolver directly, so it passes while the caller that
// matters never asks it.
func TestOriginHonoursTransportSettingsOnlyTheFileNames(t *testing.T) {
	pairCert, pairKey := realPair(t)
	for _, c := range []struct {
		name, toml, addr, want string
	}{
		{
			"insecure_plaintext on a LAN address, which would otherwise be https",
			"insecure_plaintext = true\n", "10.0.0.9:4777", "http://10.0.0.9:4777",
		},
		{
			"a certificate pair on loopback, which would otherwise be http",
			"tls_cert = \"" + pairCert + "\"\ntls_key = \"" + pairKey + "\"\n", "127.0.0.1:4777",
			"https://127.0.0.1:4777",
		},
		{
			"neither: the inference still applies",
			"", "127.0.0.1:4777", "http://127.0.0.1:4777",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.toml != "" {
				if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(c.toml), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DIBS_DIR", dir)
			t.Setenv("DIBS_ADDR", c.addr)
			if got := origin(); got != c.want {
				t.Errorf("origin() = %q, want %q: the daemon serves by the shared "+
					"rule, and a client that decides for itself talks to a working "+
					"board on a transport it does not speak", got, c.want)
			}
		})
	}
}

// The forwarded address states its transport too.
//
// joinerAddr was changed so the emitted address always says http or https,
// because the joining bridge otherwise re-infers one from the host and gets it
// wrong on two supported configurations. The TUNNEL branch kept returning the
// bare placeholder, which is the same defect surviving in the one case that
// function's own comment names: a loopback daemon holding a certificate pair.
// The forward carries TLS to the far end, the bridge sees a bare loopback
// address and speaks plain HTTP into it, and the recipe prints a trust step for
// a certificate that is never presented. It exits zero and cannot connect.
//
// The two tests over this path checked only that a forward and a trust step
// appeared, and both passed against that configuration.
func TestTheForwardedAddressSaysWhichTransportIsOnTheOtherEnd(t *testing.T) {
	for _, c := range []struct {
		name       string
		addr       string
		servesTLS  bool
		wantPrefix string
	}{
		{"a loopback daemon given a certificate pair", "127.0.0.1:4777", true, "https://"},
		{"the ordinary plaintext loopback daemon", "127.0.0.1:4777", false, "http://"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DIBS_ADDR", c.addr)
			got, err := joinerAddr(servedScheme(c.servesTLS))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Errorf("the joining machine is handed %q, which does not say the "+
					"transport. A forward carries whatever the daemon serves to the "+
					"far end, and the bridge on that machine infers a scheme from the "+
					"host: for a loopback address it guesses plaintext, so a TLS "+
					"daemon behind the forward is unreachable and the recipe still "+
					"exits successfully", got)
			}
			// It is still the OTHER machine's local end of the forward, not
			// this daemon's own address.
			if !strings.Contains(got, "<local-port>") {
				t.Errorf("the forwarded address %q lost its placeholder port: the local "+
					"end of a forward is the joining machine's choice", got)
			}
		})
	}
}

// realPair writes a certificate and key that actually load.
//
// The fixtures here used "/c.pem" and "/k.pem", which name nothing. That was
// fine while the loader only checked that both strings were present, and the
// pre-release review pointed out what that meant: `dibd -check` blessed a board
// that cannot start, during a takeover, right before the operator stopped the
// daemon it was replacing. The loader loads the pair now, so a fixture standing
// in for a TLS-configured daemon has to be one.
func realPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// A wildcard bind is reachable, so it gets the direct recipe.
//
// `:4777` and `0.0.0.0:4777` mean every interface, which is the one shape that
// is definitely reachable from another machine. An empty host was read as
// loopback, so a board deliberately bound wide was handed an ssh-forward recipe
// instead: unnecessary on a host that has ssh, and impossible on one that does
// not. The shared transport code already read it correctly, so the CLI and the
// daemon disagreed about the same string.
func TestAWildcardBindIsNotMistakenForLoopback(t *testing.T) {
	for _, addr := range []string{":4777", "0.0.0.0:4777", "[::]:4777"} {
		t.Run(addr, func(t *testing.T) {
			if tunnel, _ := boardShape(addr, ""); tunnel {
				t.Errorf("%s was given an ssh-forward recipe. It binds every "+
					"interface, so another machine reaches it directly; a forward is "+
					"advice that is unnecessary at best and unfollowable on a host "+
					"with no ssh", addr)
			}
		})
	}
	// And genuine loopback still gets one, so this cannot pass by never
	// forwarding anything.
	for _, addr := range []string{"127.0.0.1:4777", "localhost:4777", "[::1]:4777"} {
		if tunnel, _ := boardShape(addr, ""); !tunnel {
			t.Errorf("%s got no forward, and it is reachable from nowhere else: the "+
				"joining machine has no way in at all", addr)
		}
	}
}

// No credential-bearing request may leave for an authority the address hides.
//
// Everything before an `@` is userinfo, so
// `DIBS_ADDR=http://trusted.example@evil.example:4777` reads as trusted.example
// in every place the CLI prints it and dials evil.example. checkAddr catches
// that, and checkAddr was reachable only through mcp-config: fifteen call sites
// build requests with the shared client, and await, watch, monitor, the admin
// routes, the hook paths and several doctor probes went straight past it while
// attaching X-Dibs-Local, and sometimes the admin password.
//
// So the check moved to the ROUND TRIPPER, and this asks it the way a request
// does. A per-caller version of this test would only ever cover the callers
// somebody thought of, which is the defect itself.
func TestNoRequestCarriesCredentialsToAHiddenAuthority(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)

	req, err := http.NewRequest(http.MethodGet, "http://trusted.example@evil.example:4777/api/board", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Dibs-Local", "the-secret")
	resp, err := daemonClient(2 * time.Second).Transport.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a request to an address whose real authority is evil.example was " +
			"sent, with this board's local secret on it. Nothing downstream can " +
			"catch that: explicit http skips the trust ceremony entirely")
	}
	if !strings.Contains(err.Error(), "trusted.example") {
		t.Errorf("the refusal does not name what the address hides, so an operator "+
			"cannot see why: %v", err)
	}

	// And an ordinary address still goes through, so this cannot pass by
	// refusing everything.
	ok, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/api/board", nil)
	if err != nil {
		t.Fatal(err)
	}
	okResp, err := daemonClient(2 * time.Second).Transport.RoundTrip(ok)
	if okResp != nil {
		_ = okResp.Body.Close()
	}
	if err != nil && strings.Contains(err.Error(), "refusing to send") {
		t.Errorf("an ordinary loopback address was refused as a hidden authority: %v", err)
	}
}

// The boundary from resolved transport into the recipe.
//
// The recipe suite passes `servesTLS` and a joining address that are both
// derived from the answer each row expects, so it tests the renderer and not
// the handoff: an inverted comparison, a constant, or an address built from the
// wrong scheme would leave every row green while the shipped command printed a
// recipe for a daemon that does not exist. This is the two lines in between.
func TestTheRecipeIsGivenWhatTheTransportActuallyResolved(t *testing.T) {
	for _, c := range []struct {
		scheme string
		want   bool
	}{
		{"https", true},
		{"http", false},
		{"", false}, // unknown: a recipe must not invent TLS
	} {
		t.Run("scheme "+c.scheme, func(t *testing.T) {
			servesTLS, addr := recipeInputs(c.scheme, "hub.example:4777")
			if servesTLS != c.want {
				t.Errorf("a daemon serving %q is handed servesTLS=%v to the recipe. "+
					"The trust step and the emitted scheme both follow this, so the "+
					"printed instructions describe a different daemon",
					c.scheme, servesTLS)
			}
			if addr != "hub.example:4777" {
				t.Errorf("the joining address became %q on the way into the recipe", addr)
			}
		})
	}
}
