package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/lanes/internal/paths"
)

// configure is the first-run wizard. It exists because the alternative — making
// people discover flags to get a secure setup — is a UX failure, not a feature.
// Every question has a safe default; pressing Enter through the whole thing
// produces a correct single-machine configuration.
//
// It writes <dir>/lanes.toml, which is the same file an operator can hand-edit
// and the same file the admin UI will drive. One source of truth, three doors.
func configure(args []string) error {
	dir := paths.DataDir()
	if len(args) > 0 && args[0] != "" {
		// A flag is not a directory. `lanes configure --help` was taken as
		// dir="--help": on a terminal the wizard created a directory literally
		// named "--help", wrote lanes.toml into it, printed a tick and told you
		// to run `lanesd` — which reads ~/.lanes and had never heard of it. Off a
		// terminal it advised writing "--help/lanes.toml", a corrective action
		// that cannot work.
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("`lanes configure` takes a data directory, not %q — "+
				"run it bare to configure %s", args[0], paths.DataDir())
		}
		dir = args[0]
	}
	if !interactive() {
		return fmt.Errorf(`"lanes configure" needs an interactive terminal.
Non-interactive setup: write %s directly — every field is optional.
Example:
  addr = "0.0.0.0:4777"   # serve agents on other machines (TLS is automatic)`,
			filepath.Join(dir, "lanes.toml"))
	}
	// The path is the operator's own data dir, given by them on their own
	// machine — it is an argument, not untrusted input.
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // G703: operator-supplied path is the feature
		return err
	}
	cfgPath := filepath.Join(dir, "lanes.toml")

	fmt.Println("\nLanes — setup")
	fmt.Println("─────────────")
	if _, err := os.Stat(cfgPath); err == nil { //nolint:gosec // G703: see above
		fmt.Printf("\n%s already exists. Continuing will overwrite it.\n", cfgPath)
		if !confirm("Continue?", false) {
			fmt.Println("Nothing changed.")
			return nil
		}
	}

	fmt.Println(`
Where will agents connect from?

  1. This machine only            (default — nothing else can reach it)
  2. This machine and others       (LAN or private network)
  3. Others across the internet`)
	addr := "127.0.0.1:4777"
	switch ask("Choice [1]", "1") {
	case "2":
		addr = defaultLANAddr()
		fmt.Printf("\n  Serving on %s — Lanes will generate a TLS certificate automatically.\n", addr)
	case "3":
		addr = "0.0.0.0:4777"
		fmt.Println(`
  Serving on all interfaces. Lanes generates a TLS certificate automatically,
  but a self-signed certificate is not a substitute for thinking about exposure:
  keep this behind a firewall, a private network, or a reverse proxy you trust.`)
	}

	var b strings.Builder
	b.WriteString("# Lanes configuration. Every field is optional —\n")
	b.WriteString("# deleting this file returns Lanes to its defaults.\n\n")
	fmt.Fprintf(&b, "addr = %q\n", addr)
	b.WriteString("\n# tls_cert = \"/path/cert.pem\"   # bring your own certificate\n")
	b.WriteString("# tls_key  = \"/path/key.pem\"\n")
	b.WriteString("# insecure_plaintext = false     # never set this on an untrusted network\n")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil { //nolint:gosec // G703: see above
		return err
	}

	fmt.Printf("\n✓ Wrote %s\n", cfgPath)
	fmt.Println("\nNext:")
	fmt.Println("  lanesd                 start the daemon")
	fmt.Println("  lanes mcp-config       print the config to paste into each agent")
	if addr != "127.0.0.1:4777" {
		fmt.Println("\nThe daemon generates its certificate on first start; run it once before")
		fmt.Println("`lanes mcp-config` so the printed config can include the certificate path.")
	}
	fmt.Println()
	return nil
}

// interactive reports whether we can hold a conversation with a human. A daemon
// under systemd/launchd/Docker has no TTY, and must never block on a prompt.
func interactive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fo, err := os.Stdout.Stat()
	return err == nil && fo.Mode()&os.ModeCharDevice != 0
}

func ask(prompt, def string) string {
	fmt.Printf("%s: ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return def
	}
	if s := strings.TrimSpace(line); s != "" {
		return s
	}
	return def
}

func confirm(prompt string, def bool) bool {
	suffix := " [y/N]"
	if def {
		suffix = " [Y/n]"
	}
	switch strings.ToLower(ask(prompt+suffix, "")) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// defaultLANAddr suggests this host's private address so the operator does not
// have to go and look it up.
func defaultLANAddr() string {
	if ip := privateIPv4(); ip != "" {
		return ip + ":4777"
	}
	return "0.0.0.0:4777"
}

// privateIPv4 returns this host's first RFC1918 address, or "".
func privateIPv4() string {
	addrs, err := netInterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ip := a.To4()
		if ip == nil || ip.IsLoopback() {
			continue
		}
		switch {
		case ip[0] == 10,
			ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31,
			ip[0] == 192 && ip[1] == 168:
			return ip.String()
		}
	}
	return ""
}

func netInterfaceAddrs() ([]net.IP, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		as, _ := i.Addrs()
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok {
				out = append(out, n.IP)
			}
		}
	}
	return out, nil
}
