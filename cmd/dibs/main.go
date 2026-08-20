// Command dibs is the human window into the board: inspect state, follow
// the live event stream, verify ledger integrity.
package main

import (
	"bufio"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/humanauth"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/ui"
)

const usage = `dibs: window into the agent coordination board

agent-safe (agent-scoped or public, fine to run from any agent):
  dibs await              block until events arrive for YOUR agent, then exit 0
                           (token from DIBS_TOKEN; --since N, --timeout 30m,
                           run as a background task and your harness wakes you)
  dibs probe --pid N      is a subagent you spawned working, thinking or stuck?
                           (--until stuck,exited blocks and exits when it is,
                           run as a background task and your harness wakes you)
  dibs watch --exec CMD   stay running; run CMD on each message for YOUR agent
                           (--types notify,question,... ; --once ; the summoner)
  dibs monitor --agent N   own+watch a persistent agent; print a line per message
                           (generic tail for humans/scripts. Dibs itself never
                           wires this into a harness; agents use await_events)
  dibs board              public board: agents, slots, claims (--json)
  dibs log [--follow]     public event stream (private bodies stay encrypted;
                           --json: one object per event)
  dibs verify [path]      verify ledger hash chain (--json)
  dibs trust <host:port>  accept a remote daemon's certificate, so this machine
                           can reach a board on another one (compare against
                           dibs fingerprint run over there before relying on it)
  dibs fingerprint        this daemon's certificate fingerprint, to read out to
                           a machine that is trusting it
  dibs doctor             find what is quietly broken: stale harness secrets,
                           matching that is off or still indexing, a ledger that
                           will not replay. Names the fix, not just the fault
                           (--json: the same checks as one document)
  dibs calibrate          measure work-overlap scoring against THIS repo's git
                           history and propose thresholds (nothing is written)
  dibs version           print the version (also --version)
  dibs help              this text (also --help, -h)

called BY harness integrations, not by you (listed so a config that names one
is not a mystery):
  dibs mcp-stdio          stdio bridge for a host with no HTTP MCP client
  dibs hook-spawn         PreToolUse hook: stamps a spawned subagent with the
                           agent that spawned it, so a stall can be reported to
                           the agent that caused it. Reads the hook payload on
                           stdin; prints nothing and exits 0 unless it has
                           something to rewrite

setup:
  dibs configure          first-run wizard: picks secure defaults for you
  dibs configure --service write a launchd/systemd unit so the daemon survives a
                           closed terminal and a reboot; prints the load command
                           rather than running it
  dibs man                print this manual as an mdoc(7) page, generated from
                           this very help text; releases run it to ship agents.1
                           (--out FILE writes it, --date D pins the page date)
  dibs completion SHELL   print verb completions for bash, zsh or fish,
                           generated from the live verb table so the script
                           cannot drift; the README says where each shell loads it
  dibs stop               stop the daemon serving THIS data directory, and only
                           that one, not "pkill dibd", which also kills the
                           isolated daemons other fleets are running. SIGTERM, so
                           the ledger closes and claims are released; waits for
                           the process to go, so you can start a replacement
  dibs upgrade            move the running daemon onto the dibd you have
                           installed. Proves the new binary can rebuild this
                           board BEFORE stopping anything, repoints a service
                           unit pinning the wrong daemon, restarts, and checks
                           the fleet came back at the same serial. No agent
                           re-registers (-n dry-run; --adopt-dir also renames a
                           data directory an older version named)

human/admin (interactive terminal; the god-view needs the admin password):
  dibs messages           ALL mail, decrypted: prompts admin password
  dibs web                open the board: one-time link, unlocked with Touch ID
                           where the machine has it (--password to type one instead)
  dibs admin set-password  set/replace the admin password that gates the board
  dibs mcp-config         print MCP host config (contains the local secret)

env: DIBS_ADDR (default 127.0.0.1:4777), DIBS_DIR, DIBS_TOKEN,
     DIBS_ADMIN=1 (bypass the terminal check: for humans scripting)`

var version = build.Version

// styledUsage renders the help text with the same vocabulary as everything
// else. Styling at print time rather than baking escapes into the constant
// keeps the source readable and lets the whole thing collapse to plain text
// when this is piped into a pager, a file, or `grep`.
//
// Three weights and no more: the section headings a reader scans for, the verb
// they are going to type, and everything else. A help screen where every line
// competes is a help screen nobody finishes.
func styledUsage() string {
	var b strings.Builder
	for _, line := range strings.Split(usage, "\n") {
		switch {
		case strings.HasSuffix(line, ":"):
			b.WriteString(ui.Bold(line))
		case strings.HasPrefix(line, "  dibs "):
			// Split the invocation from its explanation: the command is what
			// the reader is hunting for, the rest is why they would want it.
			rest := line[len("  dibs "):]
			i := strings.Index(rest, "  ")
			if i < 0 {
				b.WriteString("  " + ui.Accent("dibs "+rest))
				break
			}
			b.WriteString("  " + ui.Accent("dibs "+rest[:i]) + ui.Dim(rest[i:]))
		case strings.HasPrefix(line, "     ") || strings.HasPrefix(line, "env:"):
			b.WriteString(ui.Dim(line))
		default:
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println(styledUsage())
		return
	}
	var err error
	switch os.Args[1] {
	case "board":
		err = board(os.Args[2:])
	case "messages":
		err = adminOnly("messages", messages)
	case "log":
		err = logCmd(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "stop":
		err = stop(os.Args[2:])
	case "upgrade":
		err = upgradeCmd(os.Args[2:])
	case "trust":
		err = trustCmd(os.Args[2:])
	case "fingerprint":
		err = fingerprintCmd(os.Args[2:])
	case "doctor":
		err = doctor(os.Args[2:])
	case "calibrate":
		err = calibrate(os.Args[2:])
	case "mcp-config":
		err = adminOnly("mcp-config", func() error { return mcpConfig(os.Args[2:]) })
	case "admin":
		err = adminCmd(os.Args[2:])
	case "await":
		err = await(os.Args[2:])
	case "hook-spawn":
		err = hookSpawn(os.Args[2:])
	case "probe":
		err = probe(os.Args[2:])
	case "watch":
		err = watch(os.Args[2:])
	case "man":
		err = man(os.Args[2:])
	case "completion":
		err = completion(os.Args[2:])
	case "configure":
		err = configure(os.Args[2:])
	case "monitor":
		err = monitor(os.Args[2:])
	case "mcp-stdio":
		err = mcpStdio(os.Args[2:])
	case "web":
		err = adminOnly("web", func() error { return webURL(os.Args[2:]) })
	case "version", "--version", "-V":
		printVersion()
	case "help", "--help", "-h":
		// Asking a tool to explain itself is not a failure. This landed in
		// `default:` and exited 2 while printing the help, so a docs step under
		// `set -e` aborted on `dibs --help`, and `dibs --version` printed
		// forty-four lines of usage containing no version at all. git, cargo and
		// rg all answer both and exit 0.
		fmt.Println(styledUsage())
	default:
		// Name the word we did not understand, and the near miss if there is one.
		//
		// This used to print the whole usage to STDOUT and exit 2, which has two
		// costs. `dibs borad 2>/dev/null` was byte-identical to `dibs --help`,
		// so a typo in a script looked like success. And telling a reader to go
		// and find the verb they believe they just typed is not help: the same
		// reasoning as nearestAgentsHint, which answers a misaddressed message
		// with the closest live agent rather than the whole board.
		fmt.Fprintf(os.Stderr, "dibs: unknown command %q\n", os.Args[1])
		if near := nearestCommand(os.Args[1]); near != "" {
			fmt.Fprintf(os.Stderr, "  did you mean: dibs %s\n", near)
		}
		fmt.Fprintln(os.Stderr, "  dibs help   lists every command")
		os.Exit(2)
	}
	if err != nil {
		// A help request reaches here through flag.ContinueOnError. It is not an
		// error: printing `dibs: flag: help requested` and exiting 1 leaked a Go
		// internals string and told a reader their question had failed.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		var exitOnly interface{ exitOnly() }
		if errors.As(err, &exitOnly) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "dibs:", err)
		os.Exit(1)
	}
}

// parseFlags is the one place a subcommand's flags are parsed.
//
// Three separate wrongs met here, each wrong differently. `--help` came back as
// flag.ErrHelp and was printed as `dibs: flag: help requested` with exit 1: a
// Go internals string, reporting a question as a failure. A mistyped flag was
// printed TWICE, once by flag's own output and once by main's `agents:` printer.
// And both went to stderr, so `dibs await --help | less` showed a blank screen.
//
// So flag stays silent and we do the printing: help to stdout at exit 0, because
// that is where a reader pipes it; a bad flag once, to stderr, naming the
// command so `-sinc` does not send somebody hunting through the wrong page.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("usage: dibs " + fs.Name())
			fs.SetOutput(os.Stdout)
			fs.PrintDefaults()
			return flag.ErrHelp
		}
		fmt.Fprintf(os.Stderr, "dibs %s: %v\n", fs.Name(), err)
		fmt.Fprintf(os.Stderr, "  dibs %s --help   lists the flags it takes\n", fs.Name())
		os.Exit(2)
	}
	return nil
}

// commands is every verb main dispatches, for the unknown-command hint. Kept
// beside the switch it mirrors; admin_test.go already reads those case labels
// out of this file, so a verb added there and forgotten here is visible.
var commands = []string{
	"await", "probe", "watch", "monitor", "board", "log", "verify", "doctor",
	"calibrate", "version", "help", "man", "completion", "configure", "messages",
	"web", "admin",
	"mcp-config", "mcp-stdio", "hook-spawn",
}

// nearestCommand picks the closest verb to what was typed, or "" when nothing
// is close enough to be worth guessing at.
//
// Deliberately conservative. A wrong suggestion is worse than none: it sends
// somebody to read the wrong page, and the reader cannot tell a guess from a
// correction. Substring either way catches the common slips (`dibs boar`,
// `dibs verif`), and one transposition or one wrong letter catches `borad` and
// `verifu`: beyond that, silence and a pointer to `dibs help`.
func nearestCommand(typed string) string {
	w := strings.ToLower(typed)
	for _, c := range commands {
		if strings.Contains(c, w) || strings.Contains(w, c) {
			return c
		}
	}
	best, bestDist := "", 3 // 3 = "not close"; only 1 and 2 are suggestions
	for _, c := range commands {
		if d := editDistance(w, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// editDistance is Levenshtein, two rows. Small and local because the only
// alternative is a dependency for eighteen short words.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func addr() string {
	a := os.Getenv("DIBS_ADDR")
	if a == "" {
		return "127.0.0.1:4777"
	}
	// A scheme is accepted here so one variable can carry a whole origin, but
	// callers that want host:port get host:port.
	if _, rest, found := strings.Cut(a, "://"); found {
		return rest
	}
	return a
}

// origin is the daemon's base URL, scheme included.
//
// The scheme is NOT a separate setting, because it is not a free choice: the
// daemon serves plaintext on loopback and TLS on anything else (see
// resolveTransport in cmd/dibd/config.go), so a client that guessed the other
// way simply cannot connect. Deriving it from the same rule means the two
// agree by construction rather than by the operator configuring both to match.
//
// Every request in this binary used to be built as origin(), in
// eighteen places. That is correct for loopback and wrong for every other
// address, so the moment a daemon was moved off 127.0.0.1 to serve a second
// machine, its own CLI could no longer talk to it: `dibs board`, `dibs doctor`
// and the rest failed against a daemon that was working perfectly.
//
// An explicit scheme in DIBS_ADDR wins, for the one case the rule cannot infer:
// a daemon deliberately serving plaintext off-loopback (insecure_plaintext).
// The two schemes, named rather than written inline. A find-and-replace over
// `"http://" + addr()` is exactly how this function's own body was turned into
// a call to itself, which recursed until the stack ran out. Constants are not
// tidiness here; they are what puts this out of a sweep's reach.
const (
	schemePlain = "http://"
	schemeTLS   = "https://"
)

func origin() string {
	if a := os.Getenv("DIBS_ADDR"); a != "" {
		if scheme, _, found := strings.Cut(a, "://"); found {
			return scheme + "://" + addr()
		}
	}
	if isLoopbackHostPort(addr()) {
		return schemePlain + addr()
	}
	return schemeTLS + addr()
}

// isLoopbackHostPort mirrors the daemon's own loopback test. A host it cannot
// parse is treated as remote: assuming plaintext for something unrecognised is
// the failure that cannot be undone by a retry.
func isLoopbackHostPort(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func get(path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, origin()+path, nil)
	if err != nil {
		return err
	}
	if s, err := localSecret(); err == nil {
		req.Header.Set("X-Dibs-Local", s)
	}
	resp, err := daemonClient(0).Do(req)
	if err != nil {
		return reachErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func localSecret() (string, error) {
	b, err := os.ReadFile(filepath.Join(paths.DataDir(), "local.secret"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// getGodView fetches a god-view endpoint (decrypted mail) using the local
// secret plus the admin password header.
func getGodView(path, adminPass string, v any) error {
	req, err := http.NewRequest(http.MethodGet, origin()+path, nil)
	if err != nil {
		return err
	}
	if s, serr := localSecret(); serr == nil {
		req.Header.Set("X-Dibs-Local", s)
	}
	req.Header.Set("X-Dibs-Admin", adminPass)
	resp, err := daemonClient(0).Do(req)
	if err != nil {
		return reachErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func mcpConfig(args []string) error {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	board := fs.String("board", "", "print the config for joining ANOTHER machine's board, "+
		"given its address as seen from here (e.g. 127.0.0.1:4777 through an ssh forward)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *board != "" {
		return printJoinConfig(*board)
	}
	s, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}
	// If the daemon generated a certificate, it is serving HTTPS: say so, and
	// hand over the certificate path. A self-signed cert that clients cannot
	// find is the difference between "works" and "mysteriously refuses".
	scheme, certPath := "http", ""
	if p := filepath.Join(paths.DataDir(), "tls-cert.pem"); fileExists(p) {
		scheme, certPath = "https", p
	}
	url := scheme + "://" + addr() + "/mcp"

	// STDIO FIRST, for the same reason it is first for Codex.
	//
	// This printed the url form for Claude Code, and every word of the Codex
	// note below applies here too: Claude Code's HTTP transport has no
	// per-session process either, so nothing holds this agent's nonce, so every
	// returning session registers as a sibling that cannot read its
	// predecessor's mail. The daemon knows and says so at registration, which is
	// to its credit and beside the point: an operator who does what this command
	// prints should not land in a state the daemon then diagnoses.
	//
	// plugins/claude-code/.mcp.json already configures `dibs mcp-stdio`, so the
	// plugin and this command were recommending different transports for one
	// client, and the one an operator reaches first was the one the plugin does
	// not use. Reported by an operator who followed this and got the warning.
	stdioCfg := map[string]any{
		"mcpServers": map[string]any{
			"dibs": map[string]any{
				"command": self(),
				"args":    []string{"mcp-stdio"},
			},
		},
	}
	stdioOut, _ := json.MarshalIndent(stdioCfg, "", "  ")
	fmt.Println("# Claude Code and JSON-config hosts: add to .mcp.json:")
	fmt.Println(string(stdioOut))
	fmt.Println()
	fmt.Println("# Better still: the plugin, which brings the wake hooks and the skill")
	fmt.Println("#   /plugin marketplace add agenxy/dibs")
	fmt.Println("#   /plugin install dibs@dibs")
	fmt.Println()

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"dibs": map[string]any{
				"type":    "http",
				"url":     url,
				"headers": map[string]string{"X-Dibs-Local": s},
			},
		},
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println("# Only where a client cannot run a process at all. NOT the answer for another")
	fmt.Println("# machine: run the bridge there instead (see the end of this output). A url")
	fmt.Println("# client holds no nonce, so each session forks an identity that cannot read")
	fmt.Println("# its predecessor's mail, and on an unattended remote session that costs most.")
	fmt.Println(string(out))
	// The Bearer line shows a prefix so a reader can see it is the same secret
	// as the header above. Unguarded, that slice panicked on any secret shorter
	// than the preview: a hand-edited or truncated local.secret took down the
	// one command whose job is to help you configure a client.
	preview := s
	if len(s) > 16 {
		preview = s[:16] + "…"
	}
	// STDIO for Codex, not HTTP, and the difference is an identity rather than a
	// preference.
	//
	// This printed the HTTP form, and Codex took it, and the cost was invisible
	// for months: an HTTP client has no per-session process, so there is nothing
	// to remember an agent's nonce, so every returning session registers as a
	// sibling that cannot read its predecessor's mail. A board carrying nine
	// rows for five roles is what that looks like from outside.
	//
	// The bridge is a process per session with a filesystem, which is exactly
	// what the credential needs and exactly what HTTP does not have. Codex has
	// supported stdio all along; almost every other server in a real config uses
	// it, and Dibs was the odd one out because this line said to be.
	//
	// The HTTP form stays documented below, because it is right for a client on
	// another machine, where a local bridge is not an option and a forked
	// identity is the lesser problem.
	fmt.Printf(`
# Codex / ChatGPT desktop: add to ~/.codex/config.toml:
[mcp_servers.dibs]
command = %q
args = ["mcp-stdio"]
# MCP 2026-07-28. Codex speaks it, but only when BOTH the mcp_2026_07_28
# feature is enabled AND this exact variable is set on THIS server entry: the
# feature alone leaves the connection on 2025-06-18, and a wrong value here is
# a hard error rather than a fallback.
env = { CODEX_MCP_PROTOCOL_VERSION = "2026-07-28" }

# stdio rather than a url, deliberately: the bridge is one process per session,
# and it remembers this agent's nonce so a returning session reattaches to the
# same identity instead of forking a "-2" sibling that cannot read its own mail.
# That is a default and not a cage: an identity can also be pinned here with
#   env = { DIBS_AGENT_NONCE = "..." }
# which is what lets an HTTP client reattach too.
#
# NOT for another machine either: run the bridge THERE, pointed here. See the
# block below, or run "dibs mcp-config --board %s" on that machine. The url
# form is for a client that genuinely cannot run a process, and it costs an
# identity per session:
#   url = %q
#   http_headers = { "X-Dibs-Local" = %q }
#   (the secret is also accepted as: Authorization: Bearer %s)
#
# Running agent sessions do not hot-load MCP config: start a new session after adding.
`, self(), addr(), url, s, preview)

	if certPath != "" {
		fmt.Printf(`
# TLS: this daemon serves HTTPS with a self-signed certificate:
#   %s
# Clients must trust it, or they will refuse the connection. Pick one:
#   • copy that file to the client machine and add it to the trust store, or
#   • point the client runtime at it:
#       Node/Claude Code : NODE_EXTRA_CA_CERTS=%s
#       Python           : SSL_CERT_FILE=%s
#       curl (testing)   : curl --cacert %s ...
`, certPath, certPath, certPath, certPath)
	}
	// Unconditionally.
	//
	// This sat inside the TLS branch, so it printed only for a daemon serving
	// HTTPS. The default daemon is plaintext on loopback, which is every fresh
	// install, so the one block explaining how a second machine joins was
	// invisible to exactly the operators who needed it. One reported nearly
	// abandoning the multi-machine board, which is the reason they run Dibs,
	// while the instructions for it were in the binary the whole time.
	printRemoteRecipe(certPath != "")
	return nil
}

// printRemoteRecipe is the setup for agents on OTHER machines.
//
// The blocks above hand a client an https URL and ask it to trust a
// self-signed certificate. That works when the runtime exposes a knob for it,
// and plenty do not: the config is accepted, the connection is refused, and the
// agent reports a server that will not start. Every one of those knobs is also
// process-wide, so trusting this daemon means changing how that whole harness
// verifies every other TLS connection it makes.
//
// The bridge avoids all of it. `dibs mcp-stdio` speaks stdio to the harness and
// verified TLS to the daemon, trusting exactly the certificate this machine
// recorded and nothing else, so the harness needs no TLS configuration and no
// certificate of its own. It is also the only shape some harnesses accept.
func printRemoteRecipe(tls bool) {
	fmt.Printf(`
# ── Agents on ANOTHER machine ───────────────────────────────────────────────
# The board is a fleet board: agents on other machines join THIS daemon and
# see the same rows. Run dibs on that machine and point its bridge here.
#
#   1. copy this machine's secret to a data directory of its own there:
#        %s   ->   ~/.dibs-<board>/local.secret   (chmod 700 the directory)
#      Its OWN ~/.dibs stays for its own board, if it runs one. The secret is
#      per-board and read from the data directory, so joining a second board
#      means a second directory; there is no way to hold two in one.
#
#   2. point the harness at the bridge, with THIS daemon's address and that
#      directory (absolute path: nothing expands ~ here):
#        {"mcpServers": {"dibs": {"command": "dibs", "args": ["mcp-stdio"],
#          "env": {"DIBS_ADDR": %q,
#                  "DIBS_DIR":  "/home/you/.dibs-<board>"}}}}
#
#      Or have that machine print it for you:
#        dibs mcp-config --board %s
#
# stdio there, NOT the url form, and on another machine it matters more rather
# than less: that session is the long-lived unattended one, and a url client
# holds no nonce, so every reconnect forks an identity that cannot read its
# predecessor's mail. The bridge is a process with a filesystem, which is what
# the credential needs.
`, filepath.Join(paths.DataDir(), "local.secret"), addr(), addr())

	if !tls {
		// The tunnel, for the daemon this actually is.
		//
		// A plaintext loopback daemon is unreachable from another host, and the
		// documented answer was a TLS endpoint: fine when the hub has a routable
		// address, and useless on the corporate and lab networks that are full of
		// hosts that will never have one. The forward has always worked, because
		// the bridge only ever talks to an address, and nothing said so. It is
		// also the better default: the daemon never leaves loopback, and the
		// tunnel authenticates the machine before Dibs sees a byte.
		fmt.Printf(`
# This daemon is plaintext on loopback, so it is unreachable from another
# host directly. Forward a port to it instead, which is supported and is the
# more private shape: nothing about this daemon is exposed to the network.
#
#   on the other machine, keep this running (or use ssh -f, or autossh):
#     ssh -N -L %s:%s %s@%s
#
#   then DIBS_ADDR above is just %s: its own end of the forward.
#
# Pick the hub deliberately: whichever machine hosts the daemon decides
# whether the fleet has a board at all. A laptop is the tempting choice and
# the wrong one, because it sleeps, changes networks and gets rebooted
# mid-task. An always-on headless host reached by a forward is the answer.
`, port(addr()), addr(), os.Getenv("USER"), hostName(), addr())
		return
	}
	fmt.Printf(`# This daemon serves HTTPS, so that machine must trust its certificate. The
# bridge holds the trust itself, so nothing else there changes:
#     dibs trust %s
# Compare what that prints against `+"`dibs fingerprint`"+` run HERE; they must match.
`, addr())
}

// port is the ":4777" half of an address, for the near end of a forward.
func port(a string) string {
	if _, p, err := net.SplitHostPort(a); err == nil {
		return p
	}
	return a
}

// hostName is this machine, for the ssh target in the recipe. A guess an
// operator can correct beats a placeholder they have to decode.
func hostName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this-machine"
	}
	return h
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// webURL mints a single-use link to the board.
//
// The finger first, and the password only if the machine cannot take one.
// Both prove the same thing, that a human is here rather than an agent holding
// the local secret, and the fingerprint proves it better: a password is a
// secret an agent could in principle have been handed. Asking for one on a Mac
// with a working sensor made the operator invent, store and type a credential
// in order to be trusted LESS than the panel already trusts them.
//
// The check itself happens in the daemon. This process only asks for it: a
// client that ran the check and reported the result would be asserting presence
// rather than proving it, and every agent on the machine can make that
// assertion.
func webURL(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	usePassword := fs.Bool("password", false,
		"use the admin password even where Touch ID is available")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}
	// A sheet nobody can reach is worse than a prompt.
	//
	// Presence is only offered when a person is actually at this terminal.
	// Piping a password IS the caller saying they cannot touch a sensor: a
	// script, a CI run, an ssh session. Raising the system sheet at them blocks
	// for ninety seconds and then fails, and the credential they supplied was
	// sitting on stdin the whole time.
	//
	// stdin only, deliberately: `dibs web | pbcopy` is an ordinary thing to do
	// and says nothing about whether somebody is sitting here.
	if humanauth.Available() && !*usePassword && atATerminal() {
		fmt.Fprintln(os.Stderr, "# Confirming it is you, on the system sheet.")
		out, err := mintBoard(s, "", true)
		if err == nil {
			return printBoardLink(out)
		}
		// Any failure falls through to the password. This command must not
		// dead-end.
		//
		// The tempting rule is to stop on a decline, on the grounds that somebody
		// who just said no should not immediately be asked for a credential.
		// That is right about a real decline and wrong about everything else it
		// cannot be told apart from: the helper reports "declined" for a cancel,
		// a failed match, AND for its own timeout, so a sheet that never reached
		// the screen is indistinguishable from a person refusing one. Measured
		// on the machine this was written on: evaluatePolicy accepted the policy,
		// reported Touch ID present, and never called back at all.
		//
		// Stopping there would leave the operator with ninety seconds of nothing
		// and no way in, on a command whose entire job is to let them in. So the
		// reason is printed, plainly, and the other door is opened. A person who
		// genuinely meant to cancel can press ctrl-c at the prompt, which costs
		// them one keystroke; the alternative costs somebody their board.
		fmt.Fprintf(os.Stderr, "# %v\n# Falling back to the admin password.\n",
			strings.TrimSpace(err.Error()))
	}
	adminPass, err := promptAdminForGodView()
	if err != nil {
		return err
	}
	out, err := mintBoard(s, adminPass, false)
	if err != nil {
		return err
	}
	return printBoardLink(out)
}

// atATerminal reports whether a person is typing at this process, which is the
// question "can they answer a prompt", not "is the output going to a screen".
func atATerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// errNoPresenceHere is the daemon saying this machine cannot check presence at
// all.
//
// The CLI falls back on every failure, so it no longer branches on this, but the
// distinction is still worth carrying: it is the one presence answer that is not
// about a person, and it is what the daemon's 412 means to anything else that
// learns to call /bootstrap.
var errNoPresenceHere = errors.New("this machine cannot check presence")

type boardGrant struct {
	BT     string `json:"bt"`
	Proof  string `json:"proof"`
	Mocked string `json:"mocked"`
}

// mintBoard asks the daemon for a one-time bootstrap token. The durable secret
// never enters the URL.
func mintBoard(secret, adminPass string, presence bool) (boardGrant, error) {
	var out boardGrant
	req, err := http.NewRequest(http.MethodPost, origin()+"/bootstrap", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("X-Dibs-Local", secret)
	if presence {
		req.Header.Set("X-Dibs-Presence", "1")
	} else {
		req.Header.Set("X-Dibs-Admin", adminPass)
	}
	// No client deadline on the presence path: the person has ninety seconds to
	// reach the sensor and the daemon owns that bound. A shorter one here would
	// cancel the request out from under a sheet they were still looking at.
	resp, err := daemonClient(0).Do(req)
	if err != nil {
		return out, reachErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return out, errNoPresenceHere
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if msg := strings.TrimSpace(string(body)); msg != "" {
			return out, errors.New(msg)
		}
		return out, fmt.Errorf("bootstrap failed: %s", resp.Status)
	}
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func printBoardLink(out boardGrant) error {
	fmt.Printf("http://%s/?bt=%s\n", addr(), out.BT)
	if out.Mocked != "" {
		fmt.Fprintln(os.Stderr, "\n# "+out.Mocked)
	}
	how := "the admin password"
	if out.Proof == "presence" {
		how = "your fingerprint"
	}
	fmt.Fprintln(os.Stderr, "\n# Single-use link, expires in 2 minutes, unlocked with "+how+
		". It sets a session cookie; the secret is never in the URL.")
	return nil
}

// The shapes `dibs board` reads. Named rather than anonymous so each section
// can be rendered by its own function: a single board() that drew all three
// scored 78 on the complexity gate, which is a fair reading of how much it was
// doing.
//
// Every field carries its wire name explicitly, because these structs face
// both ways now: they decode the daemon's payload and, under --json, they ARE
// the document. Decoding tolerated Go's case-insensitive match; encoding does
// not, and `"ID"` in a document whose source spelled it `"id"` would be a new
// name for an old fact.
type (
	boardSlot struct {
		ID   string   `json:"id"`
		Text string   `json:"text"`
		Dirs []string `json:"dirs,omitempty"`
	}
	boardAgent struct {
		ID          string    `json:"id"`
		Name        string    `json:"name,omitempty"`
		Description string    `json:"description,omitempty"`
		DisplayName string    `json:"display_name,omitempty"`
		Status      string    `json:"status"`
		ProcAlive   bool      `json:"proc_alive"`
		StaleReason string    `json:"stale_reason,omitempty"`
		LastSeen    time.Time `json:"last_seen"`
		// Host is which machine the agent is on, blank when it is this one.
		//
		// On a single-machine board it is noise, which is why it was never
		// shown. On a fleet it is the first thing a person asks: four computers
		// of agents, and a board that will not say which is which answers the
		// wrong question. Blank for local agents so the common case stays as
		// quiet as it was.
		Host string `json:"host,omitempty"`
		// Role, and whether whoever holds it can come back. A role held by an
		// agent nobody can reattach to is a power the board shows as filled and
		// nobody can use.
		Role        string `json:"role,omitempty"`
		Unreachable bool   `json:"unreachable,omitempty"`
		// Human marks the row that is the person at this machine, so an agent
		// (or `dibs board`) can find who to write to without matching on a
		// description string.
		Human bool        `json:"human,omitempty"`
		Slots []boardSlot `json:"slots"`
	}
	boardClaim struct {
		Agent   string    `json:"agent"`
		Path    string    `json:"path"`
		Mode    string    `json:"mode"`
		Note    string    `json:"note,omitempty"`
		Renewed time.Time `json:"renewed"`
	}
	boardMember struct {
		Agent string  `json:"agent"`
		Auto  bool    `json:"auto"`
		Score float64 `json:"score"`
	}
	// boardChannel is the semantic half of coordination, and the whole of what
	// v1.2 added. It was missing from this surface entirely: an operator
	// without a browser could see claims and slots but not a single agent of
	// work, nor an announcement waiting on somebody: the state that most needs
	// a person.
	boardChannel struct {
		ID        string        `json:"id"`
		Topic     string        `json:"topic,omitempty"`
		Owner     string        `json:"owner,omitempty"`
		Queue     []string      `json:"queue,omitempty"`
		Members   []boardMember `json:"members,omitempty"`
		Unacked   int           `json:"unacked_announcements"`
		Abandoned int           `json:"abandoned_announcements"`
		Blocked   int           `json:"blocked_announcements"`
		Departed  int           `json:"departed_unacked"`
	}
	boardView struct {
		Serial uint64         `json:"serial"`
		Node   string         `json:"node"`
		Agents []boardAgent   `json:"agents"`
		Claims []boardClaim   `json:"claims"`
		Spaces []boardChannel `json:"spaces"`
	}
)

func board(args []string) error {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, jsonHelp)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	var b boardView
	if err := get("/api/board", &b); err != nil {
		return err
	}
	if *asJSON {
		// One value, two renderings: the document is the same decoded view the
		// prose below draws from, so the two cannot drift apart. The top-level
		// lists are pinned to [] rather than null, because "no agents" is a
		// normal state a script iterates over, not an absent fact.
		if b.Agents == nil {
			b.Agents = []boardAgent{}
		}
		if b.Claims == nil {
			b.Claims = []boardClaim{}
		}
		if b.Spaces == nil {
			b.Spaces = []boardChannel{}
		}
		return printJSON(&b)
	}
	fmt.Println(ui.Dim(fmt.Sprintf("node %s · serial %d", b.Node, b.Serial)))

	// What a person reads before anything else: how much of the fleet is
	// working, and how much of it needs them. The browser board has carried
	// this since it existed; over ssh an operator had to count rows.
	live, working, gone := 0, 0, 0
	for _, l := range b.Agents {
		switch l.Status {
		case "active":
			live++
		case "stale", "dormant":
			gone++
		}
		if len(l.Slots) > 0 {
			working++
		}
	}
	awaiting, needsPerson := 0, 0
	for _, c := range b.Spaces {
		awaiting += c.Unacked
		needsPerson += c.Abandoned
	}
	if t := ui.Tally([]ui.Count{
		{Label: fmt.Sprintf("of %d live", len(b.Agents)), N: live, Tone: "good", Always: true},
		{Label: "declared", N: working},
		{Label: "out of touch", N: gone, Tone: "attn"},
		{Label: "awaiting ack", N: awaiting, Tone: "attn"},
		{Label: "UNANSWERED", N: needsPerson, Tone: "alarm"},
	}); t != "" {
		fmt.Println(t)
	}
	if len(b.Agents) == 0 {
		fmt.Println("\n" + ui.Dim("no agents registered: agents appear here the moment they call register"))
	}
	printAgents(b.Agents)
	printSpacesOfWork(b.Spaces)
	printClaims(b.Claims)
	return nil
}

func messages() error {
	adminPass, err := promptAdminForGodView()
	if err != nil {
		return err
	}
	var m struct {
		Messages []struct {
			Serial               uint64
			From, To, Type, Body string
			State, Response      string
			ExpireDetail         string `json:"expire_detail"`
		}
	}
	if err := getGodView("/api/messages", adminPass, &m); err != nil {
		return err
	}
	if len(m.Messages) == 0 {
		fmt.Println("no messages")
		return nil
	}
	for _, msg := range m.Messages {
		fmt.Printf("#%-5d %s → %s  %-9s %-22s %s\n", msg.Serial, msg.From, msg.To, msg.Type, msg.State, msg.Body)
		if msg.Response != "" {
			fmt.Printf("       ↳ %s\n", msg.Response)
		}
		if msg.ExpireDetail != "" {
			fmt.Printf("       ⚠ %s\n", msg.ExpireDetail)
		}
	}
	return nil
}

// logLine renders one ledger row.
//
// Three defects met on this line. The stamp was a clock with no date, so on a
// ledger spanning days serial 306 read 15:12:15 and serial 307 read 13:01:04,
// an append-only, hash-chained log appearing to run backwards, which is exactly
// the alarm `verify` exists to stop somebody raising. The op column was 18 wide
// and its widest member, activity_checkpoint, is 19, so that op ran straight
// into the agent beside it. And the padding was unconditional, so 38% of lines
// ended in run-out spaces.
func logLine(serial uint64, t time.Time, op, agent, to string) string {
	// opColW is the widest ledgered op kind: activity_checkpoint.
	const opColW = 19
	line := ui.Dim(fmt.Sprintf("%6d  %s", serial, t.Format("01-02 15:04:05")))
	if agent == "" && to == "" {
		return line + "  " + opStyle(op) // nothing follows, so pad nothing
	}
	// Pad for alignment, then a separator REGARDLESS.
	//
	// The width is a guess at the longest op name, and the guess has now been
	// wrong twice: at 18 it collided with activity_checkpoint, and at 19 it padded
	// that op to exactly zero and ran it into the agent anyway,
	// "activity_checkpointorchestrator". Alignment is what the width is for;
	// separation must not depend on it, or the next op name longer than this
	// constant reintroduces the same unreadable line.
	line += "  " + ui.Pad(opStyle(op), opColW) + " " + ui.Accent(agent)
	if to != "" {
		line += ui.Dim(" → ") + ui.Accent(to)
	}
	return line
}

// ledgerRow is the slice of a ledger record the log surfaces read: the public
// fields, keyed as the ledger file keys them. One type for the one-shot path,
// the follower and the resync, which used to carry three copies of the same
// anonymous struct.
type ledgerRow struct {
	S  uint64    `json:"s"`
	T  time.Time `json:"t"`
	E  string    `json:"e"`
	Op struct {
		// RawMessage because `agent` carries two shapes. On most ops it is an
		// agent id; on `register` it is the DESCRIPTOR object (harness, model,
		// cwd, repo). Typed as a string, every register line failed to
		// unmarshal, and the reader dropped any line it could not parse without
		// a word: 100 ledger records rendered as 86 rows, with every
		// registration among the missing.
		//
		// That is the worst failure an audit surface has. An agent joining is
		// the event people go to the log to confirm, and a peer reported exactly
		// that: a new agent it could not corroborate anywhere.
		Agent   json.RawMessage `json:"agent"`
		AgentID string          `json:"agent_id"`
		Name    string          `json:"name"`
		To      string          `json:"to"`
	} `json:"op"`
}

// actor names the agent a row is about, from whichever field carries it.
//
// Preference order is most-specific first: a plain `agent` string is the id on
// the ops that have one, `agent_id` is the id an op names explicitly, and on a
// register the only thing present is the requested `name`.
func (r ledgerRow) actor() string {
	var id string
	if json.Unmarshal(r.Op.Agent, &id) == nil && id != "" {
		return id
	}
	if r.Op.AgentID != "" {
		return r.Op.AgentID
	}
	return r.Op.Name
}

// logRecord is one row as `dibs log --json` emits it: one object per line,
// the stream shape `dibs probe --json` already set. Named for a reader who
// never sees the prose column: the ledger's single-letter keys are a storage
// format, not an interface, and serialising them would promise a stranger's
// abbreviations to every script.
type logRecord struct {
	Serial uint64    `json:"serial"`
	Time   time.Time `json:"time"`
	Op     string    `json:"op"`
	Agent  string    `json:"agent"`
	To     string    `json:"to"`
}

// renderRow renders one ledger row for whoever is reading: the styled column
// view, or one JSON object for a parser. Both come from the same decoded row,
// so the two renderings cannot drift.
func renderRow(rec ledgerRow, asJSON bool) string {
	if !asJSON {
		return logLine(rec.S, rec.T, rec.E, rec.actor(), rec.Op.To)
	}
	out, _ := json.Marshal(logRecord{Serial: rec.S, Time: rec.T, Op: rec.E, Agent: rec.actor(), To: rec.Op.To})
	return string(out)
}

func logCmd(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	// -f as well as --follow, because that is how tail, docker logs, kubectl logs
	// and journalctl all spell it. The mode used to be sniffed positionally
	// (`os.Args[2] == "--follow"`), so `dibs log -f` dumped the entire ledger and
	// exited 0 with nothing on screen saying the flag had not been understood,
	// and `dibs log --folow` did the same.
	follow := fs.Bool("follow", false, "stay attached and print events as they arrive")
	fs.BoolVar(follow, "f", false, "shorthand for --follow")
	// A tail by default, like every other log tool.
	//
	// Bare `dibs log` printed the entire ledger. 568 lines on a board a few days
	// old, and it only grows, because the ledger is the persistence rather than a
	// rotating file. Somebody running it to see what just happened had to scroll
	// past every registration since the daemon was first started. tail, journalctl
	// and docker logs all default to the recent end for the same reason.
	//
	// The omission is REPORTED rather than silent: a log that quietly hides
	// history is worse than one that prints too much, because the second is
	// merely annoying and the first is misleading.
	limit := fs.Int("limit", 50, "show only the last N events (0 for all)")
	fs.IntVar(limit, "n", 50, "shorthand for --limit")
	asJSON := fs.Bool("json", false, jsonHelp)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("`dibs log` takes no arguments, got %q: did you mean `dibs log --follow`?", rest[0])
	}
	if !*follow {
		// One-shot: read the ledger file directly (public fields).
		warnIfDirIsNotTheAddressedDaemon("`dibs log`")
		path := ledgerPath()
		// #nosec G304 -- the path is the daemon's own data directory, chosen by the
		// operator via -dir/DIBS_DIR. Refusing to open it would mean refusing to
		// run at all.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
		var lines []string
		for sc.Scan() {
			var rec ledgerRow
			if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
				// Never silently. A dropped row is a record that happened and
				// cannot be seen, which is precisely what an audit log must not
				// do; say so and keep going, so one bad line costs one line.
				lines = append(lines, ui.Warn("  (unreadable ledger record skipped: "+err.Error()+")"))
				continue
			}
			lines = append(lines, renderRow(rec, *asJSON))
		}
		if err := sc.Err(); err != nil {
			return err
		}
		printTail(lines, *limit, *asJSON)
		return nil
	}
	return followLedger(ledgerPath(), *asJSON)
}

// followLedger tails the ledger file, printing records as they are appended.
//
// Split out of logCmd because that function had grown past the complexity the
// linter allows once this stopped being four lines of SSE, but the split is
// also honest: reading a file until interrupted has nothing to do with parsing
// flags, and the two failure modes below are subtle enough to deserve their own
// frame.
//
// Follow the LEDGER, not the event stream.
//
// This used to GET /events with the local secret. /events is a god-view route
// behind the admin password, so the daemon answered 401, the body carried no
// `id:` lines, the scanner reached EOF, and the command printed "following
// live events (^C to stop)…" and exited 0: having attached to nothing. The
// failure was indistinguishable from a fleet where nothing was happening,
// which is the exact reading somebody uses this command to obtain.
//
// The one-shot path above already reads the ledger file directly and needs no
// credential, so following it removes the mismatch instead of papering over
// it: same source, same records, same lack of auth, and no god-view surface
// involved. A live fleet-wide view with mail bodies is what `dibs web` is
// for, and that one asks for the password properly.
// #nosec G304 -- the daemon's own data directory, chosen by the operator.
func followLedger(path string, asJSON bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// Start at the end: following means what happens NEXT, and replaying the
	// whole ledger first is what --limit 0 is for.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if asJSON {
		// The banner is for a person; under --json stdout is one object per
		// line and nothing else, so it moves to stderr rather than becoming a
		// parse error in whatever is attached.
		fmt.Fprintln(os.Stderr, "following "+path+" (^C to stop)…")
	} else {
		fmt.Println("following " + path + " (^C to stop)…")
	}
	rd := bufio.NewReader(f)
	// Two things a naive tail gets wrong, both of which lose a record silently.
	//
	// A record can be HALF WRITTEN when we reach it: ReadString returns the
	// partial bytes together with the error, and discarding them means resuming
	// after a record nobody ever printed. So the fragment is held and completed
	// on the next pass.
	//
	// And the file can SHRINK. A daemon restarting after an interrupted write
	// truncates the torn tail, which leaves our offset past the new end; every
	// subsequent read then starts mid-record and the first real event after the
	// repair is dropped. Measured: serial 2 vanished and serial 3 appeared as if
	// nothing had happened. Comparing the size against our own offset is the only
	// way to notice, because a shrinking file produces no error.
	// Never advance past a record we cannot complete.
	//
	// Holding the partial bytes in a buffer and gluing the rest on later looked
	// equivalent and is not: if the file is TRUNCATED between the two reads, the
	// held fragment fuses with the beginning of a different record into
	// syntactically valid JSON. Measured: a torn `"e":"tor` merged with a fresh
	// `register` and the follower printed event `torister_lane`, a record
	// that never existed. Losing a line is bad; inventing one in an audit trail is
	// worse, and nothing downstream can tell it apart from a real one.
	//
	// So the offset is rewound instead: an incomplete read leaves the file
	// position exactly where the record starts, and the next pass re-reads it
	// whole or not at all. There is no buffer to survive a truncation.
	var last uint64
	for {
		start, serr := f.Seek(0, io.SeekCurrent)
		if serr != nil {
			return serr
		}
		start -= int64(rd.Buffered())
		line, rerr := rd.ReadString('\n')
		if rerr != nil {
			repositionAfterShortRead(f, rd, start, len(line))
			// EOF is "nothing new yet", not an ending: the writer is another
			// process and the file grows under us.
			time.Sleep(400 * time.Millisecond)
			continue
		}
		full := line
		var rec ledgerRow
		if json.Unmarshal([]byte(strings.TrimSpace(full)), &rec) != nil || rec.S == 0 {
			// A COMPLETE line that will not parse means we are reading from the
			// wrong offset, and silently dropping it is how the record we were
			// supposed to print disappears.
			//
			// Watching for the file to shrink is not enough on its own: a daemon
			// that truncates a torn tail and immediately appends can be past the
			// old offset again before the next look, so the shrink is never
			// observable and the next read lands mid-record. Detecting the
			// SYMPTOM instead of the race is what makes this robust: however we
			// got mis-positioned, malformed input at a record boundary says so.
			last = resync(f, rd, last, asJSON)
			continue
		}
		// A gap says records went past unseen: the same mis-positioning, caught
		// even when the bytes happened to parse.
		if last != 0 && rec.S > last+1 {
			last = resync(f, rd, last, asJSON)
			continue
		}
		last = rec.S
		fmt.Println(renderRow(rec, asJSON))
	}
}

// repositionAfterShortRead puts the file offset somewhere a whole record starts.
//
// Two different situations reach here and they need opposite moves. If the file
// SHRANK, the daemon repaired a torn tail and everything up to the new end is
// already seen, so resume at the end. Otherwise a record is simply mid-write, and
// the offset must go BACK to its first byte so the next pass reads it whole,
// never forward, or the record is skipped.
//
// Split out because followLedger had grown past the complexity limit, and the
// two cases read better named than nested.
func repositionAfterShortRead(f *os.File, rd *bufio.Reader, start int64, partial int) {
	if truncated(f) {
		if _, err := f.Seek(0, io.SeekEnd); err == nil {
			rd.Reset(f)
		}
		return
	}
	if partial > 0 {
		if _, err := f.Seek(start, io.SeekStart); err == nil {
			rd.Reset(f)
		}
	}
}

// resync re-reads from the start and prints everything after `last`.
//
// Cheap because it is rare: only a malformed record boundary or a serial gap
// reaches here, both of which mean the follower is reading from the wrong place
// and every subsequent line is suspect. Re-scanning is O(file) once, against
// losing records silently forever.
//
// Returns the highest serial printed, so the caller resumes from a known point.
func resync(f *os.File, rd *bufio.Reader, last uint64, asJSON bool) uint64 {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return last
	}
	rd.Reset(f)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var rec ledgerRow
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.S <= last {
			continue
		}
		last = rec.S
		fmt.Println(renderRow(rec, asJSON))
	}
	// Continue from the end the scanner reached, so the tail resumes cleanly.
	if _, err := f.Seek(0, io.SeekEnd); err == nil {
		rd.Reset(f)
	}
	return last
}

// truncated reports whether the file is now shorter than where we are reading.
//
// Called only at EOF, where the buffered reader is drained and the file offset
// therefore equals our logical position. A shrinking file raises no error: the
// reads simply return data from the wrong place, so this comparison is the only
// signal that a daemon repaired a torn tail underneath us.
func truncated(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	off, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return false
	}
	return st.Size() < off
}

// verifyReport is what `dibs verify --json` says about a ledger: the same
// verdict the prose renders, from the same chainResult, so the two cannot
// disagree. `ok` means the chain is intact; `torn` qualifies an intact chain
// whose final record is a partial write, which is deliberately NOT a failure
// (the comment on chainResult carries the reasoning).
type verifyReport struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Head  string `json:"head"`
	Torn  bool   `json:"torn"`
	Error string `json:"error,omitempty"`
	// Hint carries the corrective call beside the failure. A JSON consumer needs
	// it at least as much as a person does, and it is the surface agents read.
	Hint string `json:"hint,omitempty"`
}

func verify(args []string) error {
	// `flags`, not the usual `fs`: this function also names *fs.PathError, and
	// shadowing the io/fs package with a FlagSet turns that type into a compile
	// error that reads like a typo.
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, jsonHelp)
	// Flags are parsed properly now, which retires a real alarm: `dibs verify
	// --help` used to reach os.Open and come back as INTEGRITY FAILURE on a
	// ledger named "--help", a false alarm about the one thing this command
	// exists to reassure you about.
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	warnIfDirIsNotTheAddressedDaemon("`dibs verify`")
	path := ledgerPath()
	if flags.NArg() > 0 {
		path = flags.Arg(0)
	}
	res, err := verifyChain(path)
	if *asJSON {
		rep := verifyReport{OK: err == nil, Path: path, Lines: res.Lines, Head: res.Head, Torn: res.Torn}
		if err != nil {
			rep.Error = err.Error()
			// The prose path below tells a reader that a board which has never
			// run simply has no ledger yet. Dropping that on the JSON path left
			// `open …: no such file` and nothing else, on the surface agents
			// actually read: AGENTS.md rule 6 is that every error carries the
			// corrective call, and it does not stop applying because the output
			// is machine-readable.
			var pe *fs.PathError
			if errors.As(err, &pe) {
				rep.Hint = "a board that has never run has no ledger yet: start `dibd` " +
					"once, or pass the path to the one you meant to check"
			}
		}
		if perr := printJSON(rep); perr != nil {
			return perr
		}
		if err != nil {
			return reportedError{err}
		}
		return nil
	}
	if err != nil {
		// A ledger that cannot be READ is not a ledger that is CORRUPT, and
		// saying INTEGRITY FAILURE at a missing file sends an operator hunting a
		// breach they do not have: the exact alarm internal/verify.go's own
		// comment was written to prevent, reintroduced one layer up.
		//
		// Splitting on *fs.PathError rather than fs.ErrNotExist covers the
		// directory case too, which arrives from the Read rather than the Open.
		// Every error verifyChain produces after the file is open is a plain
		// fmt.Errorf, so a PathError here can only mean it never got that far.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return fmt.Errorf("cannot read the ledger at %s: %w\n"+
				"  a board that has never run has no ledger yet: start `dibd` once,\n"+
				"  or pass the path to the one you meant to check", path, pe.Err)
		}
		return fmt.Errorf("INTEGRITY FAILURE after %d valid lines: %w", res.Lines, err)
	}
	fmt.Printf("ok: %d lines, chain intact\nhead: %s\n", res.Lines, res.Head)
	if res.Torn {
		// Deliberately not an error, and deliberately loud enough to explain
		// itself: this is what a crash between write and fsync leaves, the op
		// was never acknowledged to its caller, and the daemon truncates it on
		// replay. Reporting it as damage sends an operator hunting a breach
		// they do not have.
		fmt.Fprintln(os.Stderr,
			"\nnote: the final record is incomplete: a write interrupted by a crash or a\n"+
				"kill, not damage to the chain. The op it would have recorded was never\n"+
				"acknowledged to the agent that sent it, and the daemon discards the partial\n"+
				"record when it next replays this ledger. Nothing to repair.")
	}
	return nil
}

// printAgents lists who is on the board and whether they are working.
func printAgents(agents []boardAgent) {
	if len(agents) == 0 {
		return
	}
	fmt.Println(ui.Section("agents"))
	// Columns sized to what is actually there, not to a guess. A fixed width
	// wide enough for "stale (process gone)" leaves a gap the width of that
	// phrase on every healthy row, which is most of them.
	nameW, statusW := 0, 0
	for _, l := range agents {
		if w := lipgloss.Width(agentLabel(l)); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(l.Status) + len(staleNote(l.StaleReason)); w > statusW {
			statusW = w
		}
	}
	for _, l := range agents {
		where := ""
		if l.Host != "" {
			where = " on " + l.Host
		}
		fmt.Printf("  %s  %s  %s\n",
			ui.Accent(ui.Pad(agentLabel(l), nameW)),
			ui.Pad(agentStatus(l), statusW),
			ui.Dim("seen "+ago(l.LastSeen)+where))
		if l.Description != "" {
			fmt.Println("    " + ui.Dim(l.Description))
		}
		for _, sl := range l.Slots {
			line := "    " + sl.Text
			if len(sl.Dirs) > 0 {
				// ui.Path, because these are the same coordination paths the
				// claims rows carry and those rows already shorten them. Grepping
				// a board for a directory found the claim and silently missed the
				// slot on that same directory, or the reverse: one board, two
				// spellings of one path.
				short := make([]string, 0, len(sl.Dirs))
				for _, d := range sl.Dirs {
					short = append(short, ui.Path(d))
				}
				line += "  " + ui.Dim("["+strings.Join(short, " ")+"]")
			}
			fmt.Println(line)
		}
	}
}

// agentLabel is what a human should read to know who this is.
//
// Usually the id. But an id is an ADDRESS and must be ASCII, so an agent named
// in a non-Latin script gets `agent`: and a fleet of them reads `agent`,
// `agent-2`, `agent-3`: correct addresses that identify nobody. Where the name
// could not become the id, show both.
func agentLabel(l boardAgent) string {
	if l.DisplayName == "" {
		return l.ID
	}
	return l.DisplayName + " (" + l.ID + ")"
}

// agentStatus weights liveness the same way the browser board does: working is
// good, a dead process is worth noticing, anything else is context.
func agentStatus(l boardAgent) string {
	status := l.Status + staleNote(l.StaleReason)
	if l.Status == "stale" && l.ProcAlive {
		status += " (hung?)"
	}
	switch {
	case l.Status == "active":
		return ui.Good(status)
	case l.StaleReason == "process_exited":
		return ui.Attn(status)
	}
	return ui.Dim(status)
}

// printSpacesOfWork lists the spaces: what work exists and who is in it.
func printSpacesOfWork(chans []boardChannel) {
	if len(chans) == 0 {
		return
	}
	fmt.Println(ui.Section("spaces"))
	chW := 0
	for _, c := range chans {
		if w := lipgloss.Width(c.ID); w > chW {
			chW = w
		}
	}
	for _, c := range chans {
		lock := ""
		if c.Owner != "" {
			lock = "  " + ui.Attn("exclusive to "+c.Owner)
		}
		fmt.Printf("  %s  %s%s\n", ui.Accent(ui.Pad(c.ID, chW)), c.Topic, lock)
		if roster := spaceRoster(c.Members); roster != "" {
			fmt.Println("    " + ui.Dim("in: ") + roster)
		}
		if len(c.Queue) > 0 {
			fmt.Println("    " + ui.Dim("waiting: ") + strings.Join(c.Queue, ", "))
		}
		if notes := announceNotes(c); notes != "" {
			fmt.Println("    " + notes)
		}
	}
}

// spaceRoster names who is in an agent, carrying the score that put an
// auto-matched agent there. §10.3 wants every auto-join explainable without a
// second call.
func spaceRoster(members []boardMember) string {
	if len(members) == 0 {
		return ""
	}
	who := make([]string, 0, len(members))
	for _, m := range members {
		if m.Auto && m.Score > 0 {
			who = append(who, fmt.Sprintf("%s(%.2f)", m.Agent, m.Score))
			continue
		}
		who = append(who, m.Agent)
	}
	return strings.Join(who, ", ")
}

// announceNotes renders the four different facts about announcements, never
// folded into one number: still asking, asking nobody who can answer, gave up
// with nobody having answered, and members who left without reading. Only the
// third needs a person now, and only it gets the alarm weight.
func announceNotes(c boardChannel) string {
	var notes []string
	if c.Unacked > 0 {
		notes = append(notes, ui.Attn(fmt.Sprintf("%d awaiting ack", c.Unacked)))
	}
	if c.Blocked > 0 {
		notes = append(notes, ui.Attn(fmt.Sprintf("%d blocked", c.Blocked))+
			ui.Dim(" (owed only by agents that are gone)"))
	}
	if c.Abandoned > 0 {
		notes = append(notes, ui.Alarm(fmt.Sprintf("%d UNANSWERED", c.Abandoned))+
			ui.Dim(": needs a person"))
	}
	if c.Departed > 0 {
		notes = append(notes, ui.Dim(fmt.Sprintf("%d left unread", c.Departed)))
	}
	return strings.Join(notes, ui.Dim(" · "))
}

// printClaims lists the paths agents have asked others to respect.
func printClaims(claims []boardClaim) {
	if len(claims) > 0 {
		fmt.Println(ui.Section("claims"))
		for _, c := range claims {
			mode := ui.Dim(c.Mode)
			if c.Mode == "exclusive" {
				mode = ui.Attn(c.Mode) // the only claim that stops anybody
			}
			fmt.Printf("  %s  %s  %s %s\n",
				ui.Pad(mode, 20), ui.Pad(ui.Path(c.Path), 56),
				ui.Accent(c.Agent), ui.Dim("("+ago(c.Renewed)+") "+c.Note))
		}
	}
}

// opStyle weights an op by what it DID, so a log somebody is scrolling through
// surfaces the handful of entries that changed somebody else's world.
//
// A ledger is mostly registrations and acks: necessary, and not what a person
// scanning for "what happened here" is looking for. The ops that take something
// away from another agent, or oblige it to answer, are the ones worth finding
// at a glance.
func opStyle(kind string) string {
	switch kind {
	case "unlock_space", "evict", "merge_spaces", "prune", "force_release":
		return ui.Alarm(kind) // a coordinator overrode somebody
	case "announce", "claim", "lock_space":
		return ui.Attn(kind) // obliges or blocks others
	case "register", "check_in", "heartbeat":
		return ui.Dim(kind) // bookkeeping
	}
	return kind
}

func staleNote(reason string) string {
	switch reason {
	case "process_exited":
		return " (process gone)"
	case "lease_lapsed":
		return " (no contact)"
	case "idle_no_activity":
		return " (idle, no pid)"
	}
	return ""
}

func ledgerPath() string { return filepath.Join(paths.DataDir(), "ledger.jsonl") }

// ago renders a timestamp the way a person says it.
//
// This printed Go's own duration string, so a board read "seen 60h37m29s ago"
// and an agent stalled overnight read "11h24m0s": figures nobody converts in
// their head, in the column a human scans first to decide whether anything is
// wrong. The web board answered the same question with "2d ago" from the start;
// the CLI simply never got the rule.
//
// Duplicated from internal/web/tags.go rather than shared, deliberately and with
// the same reasoning internal/ui gives for restating board.css's palette: these
// are two renderings of one editorial decision, and a package boundary between
// cmd/ and internal/web would be a dependency in the wrong direction. If the
// rule changes, both change, and the coherence tests already exist to notice
// two surfaces disagreeing.
func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// adminOnly gates human/admin commands behind an interactive terminal. This
// is an agent-keeper, not a wall: same-user shell access can read the data dir
// regardless (SPEC §5): the gate prevents honest agents from *drifting* into
// admin surfaces, exactly like the awareness gate prevents drift on the board.
func adminOnly(name string, fn func() error) error {
	if os.Getenv("DIBS_ADMIN") == "1" {
		return fn()
	}
	fi, err := os.Stdout.Stat()
	if err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return fn()
	}
	return fmt.Errorf(`"dibs %s" is a human/admin command and needs an interactive terminal.
Dibs: use your MCP tools instead: inbox/read_mail for mail, the board resource for state.
Agent-safe CLI: dibs await | probe | board | log | verify.
(Humans scripting: set DIBS_ADMIN=1.)`, name)
}

// warnIfDirIsNotTheAddressedDaemon prints a note when a file-reading command is
// almost certainly looking at a different install from the one DIBS_ADDR names.
//
// `dibs log` reads the ledger FILE out of DIBS_DIR; `dibs log --follow`
// attaches to the daemon at DIBS_ADDR. Two halves of one command, and with only
// the address set, which is the natural thing to do when you are pointed at a
// second daemon: they answer about two different installs, with nothing on
// screen saying so. `dibs verify` has the same shape: it checks the directory's
// chain and says nothing about the daemon you were thinking of.
//
// admin.go already found this the expensive way, where `set-password` rewrote
// the credentials of an install the operator was not addressing. The check there
// REFUSES, because writing to the wrong install is unrecoverable. Reading is not,
// so this only says which one it read: a command that refused to show you a
// ledger because an environment variable disagreed would be worse than the
// confusion it prevents.
//
// Silent when nothing is listening, and silent when the addressed daemon accepts
// this directory's secret: only an actual disagreement is worth a line.
func warnIfDirIsNotTheAddressedDaemon(what string) {
	if os.Getenv("DIBS_ADDR") == "" {
		return // no second daemon in play
	}
	secret, err := localSecret()
	if err != nil {
		return
	}
	req, rerr := http.NewRequest(http.MethodGet, origin()+"/healthz", nil)
	if rerr != nil {
		return
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, derr := daemonClient(2 * time.Second).Do(req)
	if derr != nil {
		return // nothing listening; the file is all there is
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		return // same install
	}
	fmt.Fprintf(os.Stderr,
		"note: %s read %s, which belongs to a DIFFERENT install from the daemon at\n"+
			"      %s that DIBS_ADDR names. Set DIBS_DIR to that daemon's data\n"+
			"      directory if you meant its records.\n",
		what, ledgerPath(), addr())
}

// printTail shows the recent end and says so when it trimmed.
//
// Extracted because logCmd had grown past the complexity the linter allows, and
// the tail is a separate decision from reading the ledger, but the reason it is
// SAID rather than silent is the part worth keeping together: a log that quietly
// hides history is worse than one that prints too much, because the second is
// annoying and the first is misleading. The notice goes before the output so a
// reader who pipes to head, or simply stops scrolling, still learns the view was
// trimmed.
//
// Under --json the notice still exists, on stderr: stdout has to stay one
// object per line for whatever is parsing it, but silently truncating a
// machine reader's view would be the same misdirection with a different
// victim.
func printTail(lines []string, limit int, asJSON bool) {
	shown := lines
	if limit > 0 && len(lines) > limit {
		shown = lines[len(lines)-limit:]
		notice := fmt.Sprintf("showing the last %d of %d events. `dibs log --limit 0` for all\n",
			len(shown), len(lines))
		if asJSON {
			fmt.Fprint(os.Stderr, notice)
		} else {
			fmt.Print(notice)
		}
	}
	for _, l := range shown {
		fmt.Println(l)
	}
}

// reachErr explains why the daemon could not be reached, distinguishing the
// two cases an operator must not confuse.
//
// "Nothing is listening" and "something is listening but I will not trust its
// certificate" need completely different actions, and the second was reported
// as the first: a daemon serving TLS perfectly well was described as not
// running, on the exact setup path where somebody is bringing up a second
// machine and has no other signal to go on. They would go looking for a dead
// process that is alive.
//
// The daemon self-signs off loopback by design (it stands up no CA and depends
// on no VPN), so an unknown-authority error is the EXPECTED first contact from
// a new machine, not a fault. It is a prompt to carry the fingerprint over,
// which is the same trip that carries the secret.
func reachErr(err error) error {
	var unknownAuthority x509.UnknownAuthorityError
	var certInvalid x509.CertificateInvalidError
	var hostErr x509.HostnameError
	switch {
	case errors.As(err, &unknownAuthority), errors.As(err, &certInvalid):
		return fmt.Errorf("%w\n\n"+
			"  Something IS listening on %s: the certificate is what was refused.\n"+
			"  dibd signs its own certificate off loopback, so this is what first\n"+
			"  contact from a new machine looks like, not a failure.\n\n"+
			"  Trust it the same way you got the secret, by carrying it across:\n"+
			"    dibs trust %s", err, addr(), addr())
	case errors.As(err, &hostErr):
		return fmt.Errorf("%w\n\n"+
			"  The daemon is reachable but its certificate names a different host.\n"+
			"  Use the address the certificate was issued for, or delete\n"+
			"  tls-cert.pem on that machine so it reissues for the one you use", err)
	}
	// The plain case. Written with a named format string so a find-and-replace
	// over the old wording cannot turn this line into a call to the function it
	// is inside, which is how this recursed to a stack overflow twice while
	// being written.
	return fmt.Errorf(notRunningFmt, err)
}

const notRunningFmt = "%w (is dibd running?)"

// self names this executable for a config file somebody will paste.
//
// The absolute path, resolved through symlinks, because a config that says
// "dibs" works only if the harness's PATH matches the shell's, and a harness
// launched from Finder frequently has neither ~/.local/bin nor a Homebrew
// prefix on it. That failure presents as a server that silently never starts.
func self() string {
	exe, err := os.Executable()
	if err != nil {
		return "dibs"
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		return resolved
	}
	return exe
}

// printJoinConfig is `dibs mcp-config --board <addr>`: the config for joining
// SOMEBODY ELSE'S board from this machine.
//
// The rest of this command answers "how do agents reach the daemon I am running",
// which leaves the fleet case to be assembled from three separate blocks by an
// operator who does not yet know that is what they are doing. This one answers
// the other question directly, and it is the question a second machine has.
//
// It deliberately does not read local.secret: the secret needed here belongs to
// the REMOTE board and this machine may have no daemon of its own at all.
func printJoinConfig(remote string) error {
	dir := filepath.Join(homeDir(), ".dibs-"+boardSlug(remote))
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"dibs": map[string]any{
				"command": self(),
				"args":    []string{"mcp-stdio"},
				"env": map[string]string{
					"DIBS_ADDR": remote,
					"DIBS_DIR":  dir,
				},
			},
		},
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf(`# Joining the board at %s from this machine.
#
# 1. Make the data directory and copy that board's secret into it. The secret
#    is per-board and read from the data directory, so a machine that also runs
#    its own board needs this second directory; there is no way to hold two
#    secrets in one.
#
#      mkdir -p %s && chmod 700 %s
#      scp <hub>:~/.dibs/local.secret %s/local.secret
#
# 2. If that board is a plaintext loopback daemon, which is the default, it is
#    not reachable from here directly. Forward a port to it and leave this
#    running:
#
#      ssh -N -L %s <user>@<hub>
#
#    DIBS_ADDR below is then this machine's end of the forward.
#
# 3. Add to .mcp.json (Claude Code and JSON-config hosts):
%s

# Codex / ChatGPT desktop, in ~/.codex/config.toml:
[mcp_servers.dibs]
command = %q
args = ["mcp-stdio"]
env = { DIBS_ADDR = %q, DIBS_DIR = %q, CODEX_MCP_PROTOCOL_VERSION = "2026-07-28" }

# stdio, not the url form, and from another machine that matters MORE rather
# than less: this session is the long-lived unattended one, and a url client
# holds no nonce, so every reconnect forks an identity that cannot read its
# predecessor's mail. The bridge keeps the nonce in DIBS_DIR above, which is
# what makes a returning session the same agent.
#
# Check it: dibs doctor, with the same two variables set.
`, remote, dir, dir, dir, port(remote)+":"+remote, string(out), self(), remote, dir)
	return nil
}

// homeDir is the operator's home, for a path they can paste. An empty string
// would silently produce a config rooted at "/", so say what went wrong.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "/home/you"
	}
	return h
}

// boardSlug names the data directory after the board it holds the secret for,
// so a machine on three boards has three directories it can tell apart.
func boardSlug(addr string) string {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		h = addr
	}
	if h == "" || h == "127.0.0.1" || h == "localhost" || h == "::1" {
		// A forwarded port is always loopback, so the host says nothing and the
		// port is the only thing distinguishing one board from another.
		return "board-" + p
	}
	return strings.NewReplacer(".", "-", ":", "-").Replace(h)
}
