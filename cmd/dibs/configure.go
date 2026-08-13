package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

// configure is the first-run wizard. It exists because the alternative: making
// people discover flags to get a secure setup: is a UX failure, not a feature.
// Every question has a safe default; pressing Enter through the whole thing
// produces a correct single-machine configuration.
//
// It writes <dir>/dibs.toml, which is the same file an operator can hand-edit
// and the same file the admin UI will drive. One source of truth, three doors.
const serviceHelp = `dibs configure --service: keep the daemon running

  Writes an init-system unit for the data directory in DIBS_DIR (default
  ~/.dibs), so the daemon survives a closed terminal and a reboot:

    macOS   ~/Library/LaunchAgents/org.agenxy.dibs.plist
    Linux   $XDG_CONFIG_HOME/systemd/user/dibs.service

  It writes the file and prints the command to load it. Registering a job that
  starts at login is a change to your machine, so you make it, not Dibs.

  Refuses to overwrite an existing unit: edit it in place, or delete it first.
`

// serviceCommand handles `dibs configure --service`.
//
// Separate from the wizard because it is a separate command that happens to
// share a verb, and because it reads EVERY argument before writing anything:
// `configure --service --help` used to write a LaunchAgent and report success,
// which is the same shape as `dibs stop --help` stopping the daemon.
func serviceCommand(rest []string) error {
	// Every argument is read before anything is decided, including help. See the
	// note on stop(): honouring help on sight made an unknown flag pass or fail
	// depending on where the user put it.
	help := false
	for _, a := range rest {
		switch a {
		case "-h", "--help", "help":
			help = true
		default:
			return fmt.Errorf("`dibs configure --service` takes no further arguments, "+
				"and %q is not one. It writes a unit for the data directory in DIBS_DIR "+
				"(or ~/.dibs) and nothing else", a)
		}
	}
	if help {
		fmt.Print(serviceHelp)
		return nil
	}
	return writeServiceUnit()
}

func configure(args []string) error {
	// `--service` is a different job from the wizard: it writes an init-system
	// unit so the daemon outlives the shell. Handled here because that is where
	// somebody setting Dibs up for real will look for it.
	//
	// The WHOLE argument vector is inspected, not just args[0]. Looking at the
	// first element alone meant `configure --service --help` wrote a LaunchAgent
	// and exited 0: the identical bug that `dibs stop --help` had, one dispatch
	// layer away, in the form that mutates the operator's machine rather than
	// Dibs' own state. That is three instances of this shape; the rule now is
	// that a command which writes outside the data directory reads every
	// argument before doing anything.
	if len(args) > 0 && args[0] == "--service" {
		return serviceCommand(args[1:])
	}
	dir := paths.DataDir()
	if len(args) > 0 && args[0] != "" {
		// A flag is not a directory. `dibs configure --help` was taken as
		// dir="--help": on a terminal the wizard created a directory literally
		// named "--help", wrote dibs.toml into it, printed a tick and told you
		// to run `dibd`: which reads ~/.dibs and had never heard of it. Off a
		// terminal it advised writing "--help/dibs.toml", a corrective action
		// that cannot work.
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("`dibs configure` takes a data directory, not %q. "+
				"run it bare to configure %s", args[0], paths.DataDir())
		}
		dir = args[0]
	}
	if !interactive() {
		return fmt.Errorf(`"dibs configure" needs an interactive terminal.
Non-interactive setup: write %s directly: every field is optional.
Example:
  addr = "0.0.0.0:4777"   # serve agents on other machines (TLS is automatic)`,
			filepath.Join(dir, "dibs.toml"))
	}
	// The path is the operator's own data dir, given by them on their own
	// machine: it is an argument, not untrusted input.
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // G703: operator-supplied path is the feature
		return err
	}
	cfgPath := filepath.Join(dir, "dibs.toml")

	fmt.Println("\nAgents: setup")
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

  1. This machine only            (default: nothing else can reach it)
  2. This machine and others       (LAN or private network)
  3. Others across the internet`)
	addr := "127.0.0.1:4777"
	switch ask("Choice [1]", "1") {
	case "2":
		addr = defaultLANAddr()
		fmt.Printf("\n  Serving on %s. Dibs will generate a TLS certificate automatically.\n", addr)
	case "3":
		addr = "0.0.0.0:4777"
		fmt.Println(`
  Serving on all interfaces. Dibs generates a TLS certificate automatically,
  but a self-signed certificate is not a substitute for thinking about exposure:
  keep this behind a firewall, a private network, or a reverse proxy you trust.`)
	}

	var b strings.Builder
	b.WriteString("# Dibs configuration. Every field is optional;\n")
	b.WriteString("# deleting this file returns Dibs to its defaults.\n\n")
	fmt.Fprintf(&b, "addr = %q\n", addr)
	b.WriteString("\n# tls_cert = \"/path/cert.pem\"   # bring your own certificate\n")
	b.WriteString("# tls_key  = \"/path/key.pem\"\n")
	b.WriteString("# insecure_plaintext = false     # never set this on an untrusted network\n")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil { //nolint:gosec // G703: see above
		return err
	}

	fmt.Printf("\n✓ Wrote %s\n", cfgPath)
	fmt.Println("\nNext:")
	fmt.Println("  dibd                 start the daemon")
	fmt.Println("  dibs mcp-config       print the config to paste into each agent")
	if addr != "127.0.0.1:4777" {
		fmt.Println("\nThe daemon generates its certificate on first start; run it once before")
		fmt.Println("`dibs mcp-config` so the printed config can include the certificate path.")
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
