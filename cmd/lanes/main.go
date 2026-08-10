// Command lanes is the human window into the board: inspect state, follow
// the live event stream, verify ledger integrity.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/agenxy/lanes/internal/build"
	"github.com/agenxy/lanes/internal/paths"
	"github.com/agenxy/lanes/internal/ui"
)

const usage = `lanes — window into the agent coordination board

agent-safe (lane-scoped or public, fine to run from any agent):
  lanes await              block until events arrive for YOUR lane, then exit 0
                           (token from LANES_TOKEN; --since N, --timeout 30m —
                           run as a background task and your harness wakes you)
  lanes probe --pid N      is a subagent you spawned working, thinking or stuck?
                           (--until stuck,exited blocks and exits when it is —
                           run as a background task and your harness wakes you)
  lanes watch --exec CMD   stay running; run CMD on each message for YOUR lane
                           (--types notify,question,... ; --once ; the summoner)
  lanes monitor --lane N   own+watch a persistent lane; print a line per message
                           (generic tail for humans/scripts — Lanes itself never
                           wires this into a harness; agents use await_events)
  lanes board              public board: lanes, slots, claims
  lanes log [--follow]     public event stream (private bodies stay encrypted)
  lanes verify [path]      verify ledger hash chain
  lanes doctor             find what is quietly broken — stale harness secrets,
                           matching that is off or still indexing, a ledger that
                           will not replay. Names the fix, not just the fault
  lanes calibrate          measure work-overlap scoring against THIS repo's git
                           history and propose thresholds (nothing is written)
  lanes version           print the version (also --version)
  lanes help              this text (also --help, -h)

called BY harness integrations, not by you (listed so a config that names one
is not a mystery):
  lanes mcp-stdio          stdio bridge for a host with no HTTP MCP client
  lanes hook-spawn         PreToolUse hook: stamps a spawned subagent with the
                           lane that spawned it, so a stall can be reported to
                           the agent that caused it. Reads the hook payload on
                           stdin; prints nothing and exits 0 unless it has
                           something to rewrite

setup:
  lanes configure          first-run wizard — picks secure defaults for you
  lanes configure --service write a launchd/systemd unit so the daemon survives a
                           closed terminal and a reboot; prints the load command
                           rather than running it
  lanes stop               stop the daemon serving THIS data directory, and only
                           that one — not "pkill lanesd", which also kills the
                           isolated daemons other fleets are running. SIGTERM, so
                           the ledger closes and claims are released; waits for
                           the process to go, so you can start a replacement

human/admin (interactive terminal; the god-view needs the admin password):
  lanes messages           ALL mail, decrypted — prompts admin password
  lanes web                open the board — prompts admin password, one-time link
  lanes admin set-password set/replace the admin password that gates the board
  lanes mcp-config         print MCP host config (contains the local secret)

env: LANES_ADDR (default 127.0.0.1:4777), LANES_DIR, LANES_TOKEN,
     LANES_ADMIN=1 (bypass the terminal check — for humans scripting)`

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
		case strings.HasPrefix(line, "  lanes "):
			// Split the invocation from its explanation: the command is what
			// the reader is hunting for, the rest is why they would want it.
			rest := line[len("  lanes "):]
			i := strings.Index(rest, "  ")
			if i < 0 {
				b.WriteString("  " + ui.Accent("lanes "+rest))
				break
			}
			b.WriteString("  " + ui.Accent("lanes "+rest[:i]) + ui.Dim(rest[i:]))
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
		err = board()
	case "messages":
		err = adminOnly("messages", messages)
	case "log":
		err = logCmd(os.Args[2:])
	case "verify":
		err = verify()
	case "stop":
		err = stop(os.Args[2:])
	case "doctor":
		err = doctor(os.Args[2:])
	case "calibrate":
		err = calibrate(os.Args[2:])
	case "mcp-config":
		err = adminOnly("mcp-config", mcpConfig)
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
	case "configure":
		err = configure(os.Args[2:])
	case "monitor":
		err = monitor(os.Args[2:])
	case "mcp-stdio":
		err = mcpStdio(os.Args[2:])
	case "web":
		err = adminOnly("web", webURL)
	case "version", "--version", "-V":
		printVersion()
	case "help", "--help", "-h":
		// Asking a tool to explain itself is not a failure. This landed in
		// `default:` and exited 2 while printing the help, so a docs step under
		// `set -e` aborted on `lanes --help`, and `lanes --version` printed
		// forty-four lines of usage containing no version at all. git, cargo and
		// rg all answer both and exit 0.
		fmt.Println(styledUsage())
	default:
		// Name the word we did not understand, and the near miss if there is one.
		//
		// This used to print the whole usage to STDOUT and exit 2, which has two
		// costs. `lanes borad 2>/dev/null` was byte-identical to `lanes --help`,
		// so a typo in a script looked like success. And telling a reader to go
		// and find the verb they believe they just typed is not help — the same
		// reasoning as nearestLanesHint, which answers a misaddressed message
		// with the closest live lane rather than the whole board.
		fmt.Fprintf(os.Stderr, "lanes: unknown command %q\n", os.Args[1])
		if near := nearestCommand(os.Args[1]); near != "" {
			fmt.Fprintf(os.Stderr, "  did you mean: lanes %s\n", near)
		}
		fmt.Fprintln(os.Stderr, "  lanes help   lists every command")
		os.Exit(2)
	}
	if err != nil {
		// A help request reaches here through flag.ContinueOnError. It is not an
		// error: printing `lanes: flag: help requested` and exiting 1 leaked a Go
		// internals string and told a reader their question had failed.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "lanes:", err)
		os.Exit(1)
	}
}

// parseFlags is the one place a subcommand's flags are parsed.
//
// Three separate wrongs met here, each wrong differently. `--help` came back as
// flag.ErrHelp and was printed as `lanes: flag: help requested` with exit 1 — a
// Go internals string, reporting a question as a failure. A mistyped flag was
// printed TWICE, once by flag's own output and once by main's `lanes:` printer.
// And both went to stderr, so `lanes await --help | less` showed a blank screen.
//
// So flag stays silent and we do the printing: help to stdout at exit 0, because
// that is where a reader pipes it; a bad flag once, to stderr, naming the
// command so `-sinc` does not send somebody hunting through the wrong page.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("usage: lanes " + fs.Name())
			fs.SetOutput(os.Stdout)
			fs.PrintDefaults()
			return flag.ErrHelp
		}
		fmt.Fprintf(os.Stderr, "lanes %s: %v\n", fs.Name(), err)
		fmt.Fprintf(os.Stderr, "  lanes %s --help   lists the flags it takes\n", fs.Name())
		os.Exit(2)
	}
	return nil
}

// commands is every verb main dispatches, for the unknown-command hint. Kept
// beside the switch it mirrors; admin_test.go already reads those case labels
// out of this file, so a verb added there and forgotten here is visible.
var commands = []string{
	"await", "probe", "watch", "monitor", "board", "log", "verify", "doctor",
	"calibrate", "version", "help", "configure", "messages", "web", "admin",
	"mcp-config", "mcp-stdio", "hook-spawn",
}

// nearestCommand picks the closest verb to what was typed, or "" when nothing
// is close enough to be worth guessing at.
//
// Deliberately conservative. A wrong suggestion is worse than none: it sends
// somebody to read the wrong page, and the reader cannot tell a guess from a
// correction. Substring either way catches the common slips (`lanes boar`,
// `lanes verif`), and one transposition or one wrong letter catches `borad` and
// `verifu` — beyond that, silence and a pointer to `lanes help`.
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
	if a := os.Getenv("LANES_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:4777"
}

func get(path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr()+path, nil)
	if err != nil {
		return err
	}
	if s, err := localSecret(); err == nil {
		req.Header.Set("X-Lanes-Local", s)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is lanesd running?)", err)
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
	req, err := http.NewRequest(http.MethodGet, "http://"+addr()+path, nil)
	if err != nil {
		return err
	}
	if s, serr := localSecret(); serr == nil {
		req.Header.Set("X-Lanes-Local", s)
	}
	req.Header.Set("X-Lanes-Admin", adminPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is lanesd running?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func mcpConfig() error {
	s, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet — start lanesd once first: %w", err)
	}
	// If the daemon generated a certificate, it is serving HTTPS — say so, and
	// hand over the certificate path. A self-signed cert that clients cannot
	// find is the difference between "works" and "mysteriously refuses".
	scheme, certPath := "http", ""
	if p := filepath.Join(paths.DataDir(), "tls-cert.pem"); fileExists(p) {
		scheme, certPath = "https", p
	}
	url := scheme + "://" + addr() + "/mcp"

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"lanes": map[string]any{
				"type":    "http",
				"url":     url,
				"headers": map[string]string{"X-Lanes-Local": s},
			},
		},
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println("# Claude Code and JSON-config hosts — add to .mcp.json:")
	fmt.Println(string(out))
	fmt.Printf(`
# Codex / ChatGPT desktop — add to ~/.codex/config.toml:
[mcp_servers.lanes]
url = %q
http_headers = { "X-Lanes-Local" = %q }

# The secret is also accepted as: Authorization: Bearer %s
# Running agent sessions do not hot-load MCP config — start a new session after adding.
`, url, s, s[:16]+"…")

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
	return nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func webURL() error {
	s, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet — start lanesd once first: %w", err)
	}
	adminPass, err := promptAdminForGodView()
	if err != nil {
		return err
	}
	// Mint a one-time bootstrap token: local secret (same-user) + admin
	// password (human). The durable secret never enters the URL.
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/bootstrap", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Lanes-Local", s)
	req.Header.Set("X-Lanes-Admin", adminPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is lanesd running?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("bootstrap failed: %s", resp.Status)
	}
	var out struct {
		BT string `json:"bt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	fmt.Printf("http://%s/?bt=%s\n", addr(), out.BT)
	fmt.Fprintln(os.Stderr, "\n# Single-use link, expires in 2 minutes. It sets a session cookie; the secret is "+
		"never in the URL.")
	return nil
}

// The shapes `lanes board` reads. Named rather than anonymous so each section
// can be rendered by its own function — a single board() that drew all three
// scored 78 on the complexity gate, which is a fair reading of how much it was
// doing.
type (
	boardSlot struct {
		ID, Text string
		Dirs     []string
	}
	boardLane struct {
		ID, Name, Description string
		DisplayName           string `json:"display_name"`
		Status                string
		ProcAlive             bool      `json:"proc_alive"`
		StaleReason           string    `json:"stale_reason"`
		LastSeen              time.Time `json:"last_seen"`
		Slots                 []boardSlot
	}
	boardClaim struct {
		Lane, Path, Mode, Note string
		Renewed                time.Time
	}
	boardMember struct {
		Agent string
		Auto  bool
		Score float64
	}
	// boardChannel is the semantic half of coordination, and the whole of what
	// v1.2 added. It was missing from this surface entirely: an operator
	// without a browser could see claims and slots but not a single lane of
	// work, nor an announcement waiting on somebody — the state that most needs
	// a person.
	boardChannel struct {
		ID, Topic, Owner string
		Queue            []string
		Members          []boardMember
		Unacked          int `json:"unacked_announcements"`
		Abandoned        int `json:"abandoned_announcements"`
		Blocked          int `json:"blocked_announcements"`
		Departed         int `json:"departed_unacked"`
	}
	boardView struct {
		Serial   uint64 `json:"serial"`
		Node     string `json:"node"`
		Lanes    []boardLane
		Claims   []boardClaim
		Channels []boardChannel
	}
)

func board() error {
	var b boardView
	if err := get("/api/board", &b); err != nil {
		return err
	}
	fmt.Println(ui.Dim(fmt.Sprintf("node %s · serial %d", b.Node, b.Serial)))

	// What a person reads before anything else: how much of the fleet is
	// working, and how much of it needs them. The browser board has carried
	// this since it existed; over ssh an operator had to count rows.
	live, working, gone := 0, 0, 0
	for _, l := range b.Lanes {
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
	for _, c := range b.Channels {
		awaiting += c.Unacked
		needsPerson += c.Abandoned
	}
	if t := ui.Tally([]ui.Count{
		{Label: fmt.Sprintf("of %d live", len(b.Lanes)), N: live, Tone: "good", Always: true},
		{Label: "declared", N: working},
		{Label: "out of touch", N: gone, Tone: "attn"},
		{Label: "awaiting ack", N: awaiting, Tone: "attn"},
		{Label: "UNANSWERED", N: needsPerson, Tone: "alarm"},
	}); t != "" {
		fmt.Println(t)
	}
	if len(b.Lanes) == 0 {
		fmt.Println("\n" + ui.Dim("no lanes registered — agents appear here the moment they call register_lane"))
	}
	printAgents(b.Lanes)
	printLanesOfWork(b.Channels)
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
// ledger spanning days serial 306 read 15:12:15 and serial 307 read 13:01:04 —
// an append-only, hash-chained log appearing to run backwards, which is exactly
// the alarm `verify` exists to stop somebody raising. The op column was 18 wide
// and its widest member, activity_checkpoint, is 19, so that op ran straight
// into the lane beside it. And the padding was unconditional, so 38% of lines
// ended in run-out spaces.
func logLine(serial uint64, t time.Time, op, lane, to string) string {
	// opColW is the widest ledgered op kind: activity_checkpoint.
	const opColW = 19
	line := ui.Dim(fmt.Sprintf("%6d  %s", serial, t.Format("01-02 15:04:05")))
	if lane == "" && to == "" {
		return line + "  " + opStyle(op) // nothing follows, so pad nothing
	}
	// Pad for alignment, then a separator REGARDLESS.
	//
	// The width is a guess at the longest op name, and the guess has now been
	// wrong twice: at 18 it collided with activity_checkpoint, and at 19 it padded
	// that op to exactly zero and ran it into the lane anyway —
	// "activity_checkpointorchestrator". Alignment is what the width is for;
	// separation must not depend on it, or the next op name longer than this
	// constant reintroduces the same unreadable line.
	line += "  " + ui.Pad(opStyle(op), opColW) + " " + ui.Accent(lane)
	if to != "" {
		line += ui.Dim(" → ") + ui.Accent(to)
	}
	return line
}

func logCmd(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	// -f as well as --follow, because that is how tail, docker logs, kubectl logs
	// and journalctl all spell it. The mode used to be sniffed positionally
	// (`os.Args[2] == "--follow"`), so `lanes log -f` dumped the entire ledger and
	// exited 0 with nothing on screen saying the flag had not been understood —
	// and `lanes log --folow` did the same.
	follow := fs.Bool("follow", false, "stay attached and print events as they arrive")
	fs.BoolVar(follow, "f", false, "shorthand for --follow")
	// A tail by default, like every other log tool.
	//
	// Bare `lanes log` printed the entire ledger — 568 lines on a board a few days
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
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("`lanes log` takes no arguments, got %q — did you mean `lanes log --follow`?", rest[0])
	}
	if !*follow {
		// One-shot: read the ledger file directly (public fields).
		warnIfDirIsNotTheAddressedDaemon("`lanes log`")
		path := ledgerPath()
		// #nosec G304 -- the path is the daemon's own data directory, chosen by the
		// operator via -dir/LANES_DIR. Refusing to open it would mean refusing to
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
			var rec struct {
				S  uint64    `json:"s"`
				T  time.Time `json:"t"`
				E  string    `json:"e"`
				Op struct {
					Lane string `json:"lane"`
					To   string `json:"to"`
				} `json:"op"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) == nil {
				lines = append(lines, logLine(rec.S, rec.T, rec.E, rec.Op.Lane, rec.Op.To))
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		printTail(lines, *limit)
		return nil
	}
	return followLedger(ledgerPath())
}

// followLedger tails the ledger file, printing records as they are appended.
//
// Split out of logCmd because that function had grown past the complexity the
// linter allows once this stopped being four lines of SSE — but the split is
// also honest: reading a file until interrupted has nothing to do with parsing
// flags, and the two failure modes below are subtle enough to deserve their own
// frame.
//
// Follow the LEDGER, not the event stream.
//
// This used to GET /events with the local secret. /events is a god-view route
// behind the admin password, so the daemon answered 401, the body carried no
// `id:` lines, the scanner reached EOF, and the command printed "following
// live events (^C to stop)…" and exited 0 — having attached to nothing. The
// failure was indistinguishable from a fleet where nothing was happening,
// which is the exact reading somebody uses this command to obtain.
//
// The one-shot path above already reads the ledger file directly and needs no
// credential, so following it removes the mismatch instead of papering over
// it: same source, same records, same lack of auth, and no god-view surface
// involved. A live fleet-wide view with mail bodies is what `lanes web` is
// for, and that one asks for the password properly.
// #nosec G304 -- the daemon's own data directory, chosen by the operator.
func followLedger(path string) error {
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
	fmt.Println("following " + path + " (^C to stop)…")
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
	// syntactically valid JSON. Measured — a torn `"e":"tor` merged with a fresh
	// `register_lane` and the follower printed event `torister_lane`, a record
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
		var rec struct {
			S  uint64    `json:"s"`
			T  time.Time `json:"t"`
			E  string    `json:"e"`
			Op struct {
				Lane string `json:"lane"`
				To   string `json:"to"`
			} `json:"op"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(full)), &rec) != nil || rec.S == 0 {
			// A COMPLETE line that will not parse means we are reading from the
			// wrong offset, and silently dropping it is how the record we were
			// supposed to print disappears.
			//
			// Watching for the file to shrink is not enough on its own: a daemon
			// that truncates a torn tail and immediately appends can be past the
			// old offset again before the next look, so the shrink is never
			// observable and the next read lands mid-record. Detecting the
			// SYMPTOM instead of the race is what makes this robust — however we
			// got mis-positioned, malformed input at a record boundary says so.
			last = resync(f, rd, last)
			continue
		}
		// A gap says records went past unseen — the same mis-positioning, caught
		// even when the bytes happened to parse.
		if last != 0 && rec.S > last+1 {
			last = resync(f, rd, last)
			continue
		}
		last = rec.S
		fmt.Println(logLine(rec.S, rec.T, rec.E, rec.Op.Lane, rec.Op.To))
	}
}

// repositionAfterShortRead puts the file offset somewhere a whole record starts.
//
// Two different situations reach here and they need opposite moves. If the file
// SHRANK, the daemon repaired a torn tail and everything up to the new end is
// already seen, so resume at the end. Otherwise a record is simply mid-write, and
// the offset must go BACK to its first byte so the next pass reads it whole —
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
func resync(f *os.File, rd *bufio.Reader, last uint64) uint64 {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return last
	}
	rd.Reset(f)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var rec struct {
			S  uint64    `json:"s"`
			T  time.Time `json:"t"`
			E  string    `json:"e"`
			Op struct {
				Lane string `json:"lane"`
				To   string `json:"to"`
			} `json:"op"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.S <= last {
			continue
		}
		last = rec.S
		fmt.Println(logLine(rec.S, rec.T, rec.E, rec.Op.Lane, rec.Op.To))
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
// therefore equals our logical position. A shrinking file raises no error — the
// reads simply return data from the wrong place — so this comparison is the only
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

func verify() error {
	warnIfDirIsNotTheAddressedDaemon("`lanes verify`")
	path := ledgerPath()
	if len(os.Args) > 2 {
		// A flag is not a path. `lanes verify --help` reached os.Open and came
		// back as INTEGRITY FAILURE on a ledger named "--help" — an alarm about
		// the one thing this command exists to reassure you about.
		if strings.HasPrefix(os.Args[2], "-") {
			return fmt.Errorf("`lanes verify` takes a ledger path, not %q — "+
				"run it bare to check this board's own ledger", os.Args[2])
		}
		path = os.Args[2]
	}
	res, err := verifyChain(path)
	if err != nil {
		// A ledger that cannot be READ is not a ledger that is CORRUPT, and
		// saying INTEGRITY FAILURE at a missing file sends an operator hunting a
		// breach they do not have — the exact alarm internal/verify.go's own
		// comment was written to prevent, reintroduced one layer up.
		//
		// Splitting on *fs.PathError rather than fs.ErrNotExist covers the
		// directory case too, which arrives from the Read rather than the Open.
		// Every error verifyChain produces after the file is open is a plain
		// fmt.Errorf, so a PathError here can only mean it never got that far.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return fmt.Errorf("cannot read the ledger at %s: %w\n"+
				"  a board that has never run has no ledger yet — start `lanesd` once,\n"+
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
			"\nnote: the final record is incomplete — a write interrupted by a crash or a\n"+
				"kill, not damage to the chain. The op it would have recorded was never\n"+
				"acknowledged to the agent that sent it, and the daemon discards the partial\n"+
				"record when it next replays this ledger. Nothing to repair.")
	}
	return nil
}

// printAgents lists who is on the board and whether they are working.
func printAgents(lanes []boardLane) {
	if len(lanes) == 0 {
		return
	}
	fmt.Println(ui.Section("agents"))
	// Columns sized to what is actually there, not to a guess. A fixed width
	// wide enough for "stale (process gone)" leaves a gap the width of that
	// phrase on every healthy row, which is most of them.
	nameW, statusW := 0, 0
	for _, l := range lanes {
		if w := lipgloss.Width(agentLabel(l)); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(l.Status) + len(staleNote(l.StaleReason)); w > statusW {
			statusW = w
		}
	}
	for _, l := range lanes {
		fmt.Printf("  %s  %s  %s\n",
			ui.Accent(ui.Pad(agentLabel(l), nameW)),
			ui.Pad(agentStatus(l), statusW),
			ui.Dim("seen "+ago(l.LastSeen)))
		if l.Description != "" {
			fmt.Println("    " + ui.Dim(l.Description))
		}
		for _, sl := range l.Slots {
			line := "    " + sl.Text
			if len(sl.Dirs) > 0 {
				// ui.Path, because these are the same coordination paths the
				// claims rows carry and those rows already shorten them. Grepping
				// a board for a directory found the claim and silently missed the
				// slot on that same directory, or the reverse — one board, two
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
// in a non-Latin script gets `lane` — and a fleet of them reads `lane`,
// `lane-2`, `lane-3`: correct addresses that identify nobody. Where the name
// could not become the id, show both.
func agentLabel(l boardLane) string {
	if l.DisplayName == "" {
		return l.ID
	}
	return l.DisplayName + " (" + l.ID + ")"
}

// agentStatus weights liveness the same way the browser board does: working is
// good, a dead process is worth noticing, anything else is context.
func agentStatus(l boardLane) string {
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

// printLanesOfWork lists the channels: what work exists and who is in it.
func printLanesOfWork(chans []boardChannel) {
	if len(chans) == 0 {
		return
	}
	fmt.Println(ui.Section("lanes of work"))
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
		if roster := laneRoster(c.Members); roster != "" {
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

// laneRoster names who is in a lane, carrying the score that put an
// auto-matched agent there — §10.3 wants every auto-join explainable without a
// second call.
func laneRoster(members []boardMember) string {
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
			ui.Dim(" — needs a person"))
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
				ui.Accent(c.Lane), ui.Dim("("+ago(c.Renewed)+") "+c.Note))
		}
	}
}

// opStyle weights an op by what it DID, so a log somebody is scrolling through
// surfaces the handful of entries that changed somebody else's world.
//
// A ledger is mostly registrations and acks — necessary, and not what a person
// scanning for "what happened here" is looking for. The ops that take something
// away from another agent, or oblige it to answer, are the ones worth finding
// at a glance.
func opStyle(kind string) string {
	switch kind {
	case "lane_force_release", "lane_evict", "lane_merge", "prune_lane", "force_release":
		return ui.Alarm(kind) // a coordinator overrode somebody
	case "lane_announce", "claim", "lane_exclusive":
		return ui.Attn(kind) // obliges or blocks others
	case "register_lane", "ack_board", "heartbeat":
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
// and an agent stalled overnight read "11h24m0s" — figures nobody converts in
// their head, in the column a human scans first to decide whether anything is
// wrong. The web board answered the same question with "2d ago" from the start;
// the CLI simply never got the rule.
//
// Duplicated from internal/web/tags.go rather than shared, deliberately and with
// the same reasoning internal/ui gives for restating board.css's palette: these
// are two renderings of one editorial decision, and a package boundary between
// cmd/ and internal/web would be a dependency in the wrong direction. If the
// rule changes, both change — and the coherence tests already exist to notice
// two surfaces disagreeing.
func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
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
// is a lane-keeper, not a wall: same-user shell access can read the data dir
// regardless (SPEC §5) — the gate prevents honest agents from *drifting* into
// admin surfaces, exactly like the awareness gate prevents drift on the board.
func adminOnly(name string, fn func() error) error {
	if os.Getenv("LANES_ADMIN") == "1" {
		return fn()
	}
	fi, err := os.Stdout.Stat()
	if err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return fn()
	}
	return fmt.Errorf(`"lanes %s" is a human/admin command and needs an interactive terminal.
Lanes: use your MCP tools instead — inbox/get_message for mail, the board resource for state.
Lane-safe CLI: lanes await | probe | board | log | verify.
(Humans scripting: set LANES_ADMIN=1.)`, name)
}

// warnIfDirIsNotTheAddressedDaemon prints a note when a file-reading command is
// almost certainly looking at a different install from the one LANES_ADDR names.
//
// `lanes log` reads the ledger FILE out of LANES_DIR; `lanes log --follow`
// attaches to the daemon at LANES_ADDR. Two halves of one command, and with only
// the address set — which is the natural thing to do when you are pointed at a
// second daemon — they answer about two different installs, with nothing on
// screen saying so. `lanes verify` has the same shape: it checks the directory's
// chain and says nothing about the daemon you were thinking of.
//
// admin.go already found this the expensive way, where `set-password` rewrote
// the credentials of an install the operator was not addressing. The check there
// REFUSES, because writing to the wrong install is unrecoverable. Reading is not,
// so this only says which one it read — a command that refused to show you a
// ledger because an environment variable disagreed would be worse than the
// confusion it prevents.
//
// Silent when nothing is listening, and silent when the addressed daemon accepts
// this directory's secret: only an actual disagreement is worth a line.
func warnIfDirIsNotTheAddressedDaemon(what string) {
	if os.Getenv("LANES_ADDR") == "" {
		return // no second daemon in play
	}
	secret, err := localSecret()
	if err != nil {
		return
	}
	req, rerr := http.NewRequest(http.MethodGet, "http://"+addr()+"/healthz", nil)
	if rerr != nil {
		return
	}
	req.Header.Set("X-Lanes-Local", secret)
	resp, derr := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if derr != nil {
		return // nothing listening; the file is all there is
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		return // same install
	}
	fmt.Fprintf(os.Stderr,
		"note: %s read %s, which belongs to a DIFFERENT install from the daemon at\n"+
			"      %s that LANES_ADDR names. Set LANES_DIR to that daemon's data\n"+
			"      directory if you meant its records.\n",
		what, ledgerPath(), addr())
}

// printTail shows the recent end and says so when it trimmed.
//
// Extracted because logCmd had grown past the complexity the linter allows, and
// the tail is a separate decision from reading the ledger — but the reason it is
// SAID rather than silent is the part worth keeping together: a log that quietly
// hides history is worse than one that prints too much, because the second is
// annoying and the first is misleading. The notice goes before the output so a
// reader who pipes to head, or simply stops scrolling, still learns the view was
// trimmed.
func printTail(lines []string, limit int) {
	shown := lines
	if limit > 0 && len(lines) > limit {
		shown = lines[len(lines)-limit:]
		fmt.Printf("showing the last %d of %d events — `lanes log --limit 0` for all\n",
			len(shown), len(lines))
	}
	for _, l := range shown {
		fmt.Println(l)
	}
}
