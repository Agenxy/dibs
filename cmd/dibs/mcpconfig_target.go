package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/paths"
)

// This file holds the small decisions `mcp-config` makes about WHICH daemon a
// generated configuration is for, and how a bridge is told to reach it. They
// were in main.go and each of them was got wrong at least once: the address
// handed to another process, the environment a non-default daemon needs, the
// one env line a TOML table may have, and which invocations may run without a
// terminal. Together in one place, they are readable against each other.

// mcpConfigEntry applies the interactive gate and then runs mcp-config.
//
// A function rather than a branch in the dispatch, because the branch could not
// be tested: the probe for it called joiningAnotherBoard directly, so reverting
// the dispatch to gate the whole verb, which is the regression it names, would
// have left it green. Raised by the pre-release review.
func mcpConfigEntry(args []string) error {
	if joiningAnotherBoard(args) {
		return mcpConfig(args)
	}
	return adminOnly("mcp-config", func() error { return mcpConfig(args) })
}

// joiningAnotherBoard reports whether these arguments ask for --board, which
// prints no secret of this machine's. Deliberately a plain scan rather than a
// second flag parse: it decides only whether to apply the interactive gate,
// and mcpConfig still parses and validates properly.
func joiningAnotherBoard(args []string) bool {
	for i, a := range args {
		// A VALUE is required. `--board=` passed this scan, waived the gate, and
		// then parsed as empty and fell through to the local configuration,
		// which prints this machine's secret: the waiver has to be as narrow as
		// the thing it waives for.
		for _, f := range []string{"--board", "-board"} {
			if v, ok := strings.CutPrefix(a, f+"="); ok {
				return v != ""
			}
			if a == f {
				return i+1 < len(args) && args[i+1] != "" &&
					!strings.HasPrefix(args[i+1], "-")
			}
		}
	}
	return false
}

// boardGiven reports whether --board appeared at all, which is not the same as
// its value being non-empty.
func boardGiven(fs *flag.FlagSet) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "board" {
			seen = true
		}
	})
	return seen
}

// rawAddr is DIBS_ADDR exactly as this process was given it, scheme included.
//
// addr() strips the scheme, which is right for a caller that wants host:port
// and wrong for anything handed to another process: `http://<non-loopback>` is
// how a deliberately plaintext daemon off loopback is reached, and a bridge
// given bare host:port infers HTTPS and cannot connect.
func rawAddr() string {
	if a := os.Getenv("DIBS_ADDR"); a != "" {
		return a
	}
	// Then the configuration the WIZARD wrote.
	//
	// `dibs configure` asks where agents will connect from, writes the answer to
	// dibs.toml, and its last lines say to run `dibs mcp-config`. That printed a
	// configuration for 127.0.0.1:4777 regardless, so an operator who chose LAN
	// or internet was handed a confident config for the wrong address and the
	// wrong transport, by the command the wizard had just sent them to. Found by
	// the pre-release review.
	if a := configuredAddr(paths.DataDir()); a != "" {
		return a
	}
	return addr()
}

// configuredAddr is the `addr` this data directory's dibs.toml names, or "".
//
// Only that one field: this is the CLI describing the daemon it would talk to,
// not a second implementation of the daemon's configuration, and every other
// field is the daemon's business.
func configuredAddr(dir string) string {
	a, _ := readConfiguredAddr(dir)
	return a
}

// readConfiguredAddr also reports a dibs.toml that does not parse.
//
// Swallowing that was a success response whose advertised effect is false: the
// daemon refuses to start on a malformed config, while this printed a confident
// configuration for 127.0.0.1:4777. An operator would then be looking at the
// config the CLI gave them, wondering why nothing connects, with the actual
// fault in a file neither of them mentioned.
// readBoardConfig reads this data directory's dibs.toml through the same loader
// the daemon uses, so a file dibd will not start on is one this refuses.
//
// Not quite everything: two checks need the daemon's own defaults, and stay
// with them. Whether a ceiling set here exceeds one that is NOT set is compared
// against `core.DefaultLimits()`, and a blob cap is compared against the
// maximum blob size. Neither is knowable from the file alone, so this does not
// claim them. An earlier version of this comment claimed the lot, which was
// what let `agent_ttl = "10"` through.
//
// It used to decode a four-field projection and check every other key against a
// hand-kept list of NAMES, which can only ever validate spelling: `[limits]
// agent_ttl = 10` passed here and produced a configuration, while `dibd -check`
// refused the same file because that field is a duration. The daemon would not
// start and the command telling an operator how to connect to it reported
// success. Both now call internal/boardconfig, so there is one answer.
func readBoardConfig(dir string) (boardconfig.Config, error) {
	c, err := boardconfig.Load(dir)
	if err != nil {
		return c, fmt.Errorf("%s: %w\n\nAny configuration printed from it would be a "+
			"guess: dibd refuses this file, so fix it (or run `dibd -check`) first",
			filepath.Join(dir, "dibs.toml"), err)
	}
	return c, nil
}

func readConfiguredAddr(dir string) (string, error) {
	c, err := readBoardConfig(dir)
	return c.Addr, err
}

// nonDefaultEnv is the environment a bridge needs to reach THIS daemon rather
// than the default one, and is empty when this IS the default one.
func nonDefaultEnv() map[string]string {
	env := map[string]string{}
	// VERBATIM, not addr().
	//
	// addr() strips an explicit scheme, which is the one thing DIBS_ADDR carries
	// that cannot be inferred: `DIBS_ADDR=http://<non-loopback>` is how a
	// deliberately plaintext daemon off loopback is reached, and handing the
	// bridge bare host:port makes it infer HTTPS and fail to connect. Whatever
	// this process was told is what the bridge should be told.
	if raw := os.Getenv("DIBS_ADDR"); raw != "" {
		env["DIBS_ADDR"] = raw
	} else if a := dialableAddr(rawAddr()); a != "127.0.0.1:4777" {
		// rawAddr, not addr: addr ignores dibs.toml, so a daemon configured onto
		// a LAN address or a non-default port had its stdio config printed with
		// no DIBS_ADDR at all, and the bridge then dialled 127.0.0.1:4777 and
		// could not reach it. The url block above had already been fixed and
		// this had not, which is this branch's recurring shape.
		env["DIBS_ADDR"] = a
	}
	if d := os.Getenv("DIBS_DIR"); d != "" {
		env["DIBS_DIR"] = d
	}
	return env
}

// codexEnvLine is the ONE `env = { ... }` line the Codex table gets.
//
// One line, because a TOML table may not repeat a key: this was briefly two,
// a protocol-version line and a daemon-address line, which is a duplicate-key
// error in a strict parser and a silent override in a lenient one. Whatever a
// bridge needs to reach a non-default daemon is merged in here rather than
// added beside it.
func codexEnvLine() string {
	env := nonDefaultEnv()
	// Codex speaks 2026-07-28 only when BOTH the mcp_2026_07_28 feature is
	// enabled AND this exact variable is set on THIS server entry. The feature
	// alone leaves the connection on 2025-06-18, and a wrong value here is a
	// hard error rather than a fallback.
	env["CODEX_MCP_PROTOCOL_VERSION"] = "2026-07-28"
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s = %q", k, env[k]))
	}
	return "env = { " + strings.Join(parts, ", ") + " }"
}

// trustCommand is the `dibs trust` line to print, carrying DIBS_DIR when this
// process is using one.
//
// Printed bare, an operator on a joined board pastes it without the variable
// and records the certificate in the default data directory: trust reports
// success and the bridge goes on refusing the board, which is the failure this
// message exists to end. It is only added when there is something to carry, so
// the ordinary single-board case reads as it did.
func trustCommand() string {
	// addr(), not rawAddr(): `dibs trust` dials host:port and a scheme reaches
	// tls.Dial as part of the host, failing with "too many colons in address".
	// The scheme belongs in DIBS_ADDR, which is a different argument to a
	// different program.
	cmd := "dibs trust " + shellArg(addr())
	if d := os.Getenv("DIBS_DIR"); d != "" {
		return "DIBS_DIR=" + shellArg(d) + " " + cmd
	}
	return cmd
}

// checkAddrShape refuses an address that is malformed however it is used.
//
// A scheme Dibs does not speak, a missing or non-numeric port, or a host
// carrying path segments: none of these can work as anybody's address, and each
// produced a complete-looking configuration around it. The port half also names
// the credential directory, so a path there walked that directory out of the
// operator's home.
func checkAddrShape(what, a string) error {
	if a == "" {
		return nil // absent is not malformed; the default applies
	}
	rest := a
	if scheme, r, found := strings.Cut(a, "://"); found {
		switch strings.ToLower(scheme) {
		case "http", "https":
			rest = r
		default:
			return fmt.Errorf("%s: %q is not a scheme Dibs speaks. Use http:// or "+
				"https://, or leave it off and the address decides", what, scheme)
		}
	}
	h, p, err := net.SplitHostPort(rest)
	if err != nil {
		return fmt.Errorf("%s: %q is not a host:port address: %w", what, a, err)
	}
	if n, perr := strconv.Atoi(p); perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s: %q is not a port number in %q", what, p, a)
	}
	if strings.ContainsAny(h, `/\`) || strings.Contains(h, "..") {
		return fmt.Errorf("%s: %q is not a host name", what, h)
	}
	return nil
}

// checkBoardAddr adds the rule that only applies to a board you DIAL.
//
// A wildcard is a legitimate bind address, and this daemon's own dibs.toml may
// well say 0.0.0.0: what it cannot be is the address another machine reaches a
// board on. Applying the dialable rule to both paths refused the wizard's own
// "this machine and others" answer.
func checkBoardAddr(a string) error {
	const what = "dibs mcp-config --board"
	if err := checkAddrShape(what, a); err != nil {
		return err
	}
	rest := a
	if _, r, found := strings.Cut(a, "://"); found {
		rest = r
	}
	h, _, err := net.SplitHostPort(rest)
	if err != nil {
		return err
	}
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return fmt.Errorf("%s: %q is a LISTEN address, not one this machine can dial. "+
			"Use the address you reach that board on", what, a)
	}
	return nil
}

// dialableAddr is an address a bridge can connect to, scheme preserved.
//
// A wildcard bind is the case: 0.0.0.0 is what a daemon LISTENS on and not
// something anything connects to, so handing it to a bridge as DIBS_ADDR is
// the same mistake as printing it in a url.
func dialableAddr(a string) string {
	if scheme, rest, found := strings.Cut(a, "://"); found {
		return scheme + "://" + clientHost(rest)
	}
	return clientHost(a)
}
