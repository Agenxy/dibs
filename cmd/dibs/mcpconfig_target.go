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

	"github.com/BurntSushi/toml"

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
// boardConfig is the little of dibs.toml this command needs in order to
// describe the daemon honestly: where it listens, and whether it serves TLS.
type boardConfig struct {
	Addr              string `toml:"addr"`
	InsecurePlaintext bool   `toml:"insecure_plaintext"`
	TLSCert           string `toml:"tls_cert"`
	TLSKey            string `toml:"tls_key"`
}

// readBoardConfig reads those fields, and refuses a file the daemon would.
func readBoardConfig(dir string) (boardConfig, error) {
	var c boardConfig
	path := filepath.Join(dir, "dibs.toml")
	b, err := os.ReadFile(path) // #nosec G304 -- the operator's own data dir
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("cannot read %s, so any configuration printed from it "+
			"would be a guess: %w", path, err)
	}
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return c, fmt.Errorf("%s does not parse, so this daemon will not start and "+
			"any configuration printed from it would be a guess: %w", path, err)
	}
	// The daemon refuses an unknown key, so a typo means it will not start,
	// and printing a confident configuration for the default address while
	// `dibd` exits 1 on the same file is the success-that-is-false shape again.
	// `adrr = "192.168.1.5:4777"` is a real way to lose an afternoon.
	for _, k := range md.Undecoded() {
		top, _, _ := strings.Cut(k.String(), ".")
		if !knownConfigKeys[top] {
			return c, fmt.Errorf("%s sets %q, which dibd does not know, so it will "+
				"refuse to start: fix the file (or run `dibd -check`) before taking a "+
				"configuration from it", path, top)
		}
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

// checkBoardAddr refuses an address that cannot work, rather than printing a
// complete-looking configuration around it.
//
// The shipped command accepted `htps://hub:4777`, called it plaintext because
// the scheme was not "https", and emitted the typo as DIBS_ADDR; and accepted
// `0.0.0.0:4777`, which is a listen address a client cannot dial. Both exited
// 0 and reported a complete setup.
func checkBoardAddr(a string) error {
	rest := a
	if scheme, r, found := strings.Cut(a, "://"); found {
		switch strings.ToLower(scheme) {
		case "http", "https":
			rest = r
		default:
			return fmt.Errorf("dibs mcp-config --board: %q is not a scheme Dibs speaks. "+
				"Use http:// or https://, or leave it off and the address decides", scheme)
		}
	}
	h, p, err := net.SplitHostPort(rest)
	if err != nil {
		return fmt.Errorf("dibs mcp-config --board: %q is not a host:port address: %w", a, err)
	}
	// SplitHostPort does not check that the port is one.
	//
	// `hub:not-a-port` was accepted, and worse, `hub:4777/../../escaped`: the
	// port half goes into the credential directory's name, so a path there
	// walked the directory out of the operator's home. It is a name derived
	// from untrusted-ish input that then has `mkdir`, `chmod 700` and a secret
	// written into it, which is not a shape to leave loose.
	if n, perr := strconv.Atoi(p); perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("dibs mcp-config --board: %q is not a port number in %q", p, a)
	}
	if strings.ContainsAny(h, `/\`) || strings.Contains(h, "..") {
		return fmt.Errorf("dibs mcp-config --board: %q is not a host name", h)
	}
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return fmt.Errorf("dibs mcp-config --board: %q is a LISTEN address, not one this "+
			"machine can dial. Use the address you reach that board on", a)
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

// knownConfigKeys is dibd's TOP-LEVEL configuration keys.
//
// Top-level only: anything nested under a known table is that table's business
// and the daemon validates it. This exists so a typo at the top, `adrr` for
// `addr`, is reported here rather than leaving the CLI printing a confident
// configuration for the default address while `dibd` refuses to start on the
// same file.
//
// A copy of the daemon's list, because cmd/dibd is package main and cannot be
// imported. TestConfigKeysMatchTheDaemon reads the struct out of its source, so
// the copy cannot drift.
var knownConfigKeys = map[string]bool{
	"addr": true, "tls_cert": true, "tls_key": true, "insecure_plaintext": true,
	"match": true, "limits": true, "supervise": true, "roles": true, "wake": true,
}
