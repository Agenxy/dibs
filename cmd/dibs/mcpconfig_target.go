package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
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
	return addr()
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
	} else if a := addr(); a != "127.0.0.1:4777" {
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
