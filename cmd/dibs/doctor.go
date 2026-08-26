package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/humanauth"
	"github.com/agenxy/dibs/internal/liveness"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/ui"
)

// doctor finds the things that go wrong quietly.
//
// Every check here exists because the failure it names actually happened during
// development and cost real time, and in every case the symptom was SILENCE:
// a harness that saw zero tools, a declaration that matched nothing, an agent
// that appeared on the board with no identity. None of them produced an error
// anywhere the person or the agent could see it.
//
// The output names the FIX, not the fault. "Stale secret" leaves you exactly as
// stuck; "the secret in ~/.codex/config.toml no longer matches: run `dibs
// mcp-config` and re-copy it" does not.
func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	verbose := fs.Bool("verbose", false, "also say what was checked and found healthy")
	fs.BoolVar(verbose, "v", false, "shorthand for --verbose")
	asJSON := fs.Bool("json", false, jsonHelp)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	d := &diagnosis{json: *asJSON}
	err := d.run(*verbose)
	if *asJSON {
		// One document however far the run got: the early returns inside run
		// stop the checking, never the report. Every level is carried, because
		// a monitoring script's threshold is its own business, and the checks
		// keep the order the prose prints them in.
		if perr := printJSON(doctorReport{
			DataDir:  paths.DataDir(),
			Checks:   d.checks,
			Problems: d.probs,
			Warnings: d.warns,
			Healthy:  d.probs == 0,
		}); perr != nil {
			return perr
		}
	}
	return err
}

// doctorReport is `dibs doctor --json`: the same checks the prose prints,
// as one document a monitoring probe can hold onto.
type doctorReport struct {
	DataDir  string        `json:"data_dir"`
	Checks   []doctorCheck `json:"checks"`
	Problems int           `json:"problems"`
	Warnings int           `json:"warnings"`
	Healthy  bool          `json:"healthy"`
}

// doctorCheck is one verdict: ok, warning or problem, with the fix beside the
// fault for the two levels that need one, exactly as the prose insists.
type doctorCheck struct {
	Level string `json:"level"`
	What  string `json:"what"`
	Fix   string `json:"fix,omitempty"`
}

// diagnosis collects what doctor finds, and prints as it goes unless the run
// is feeding a JSON document. One recording path for both modes, so the
// document cannot carry a different diagnosis from the prose: formatting
// twice is how they drift.
type diagnosis struct {
	json         bool
	checks       []doctorCheck
	probs, warns int
}

func (d *diagnosis) ok(what string) {
	d.checks = append(d.checks, doctorCheck{Level: "ok", What: what})
	d.prose(ui.OK(what))
}

func (d *diagnosis) bad(what, fix string) {
	d.probs++
	d.checks = append(d.checks, doctorCheck{Level: "problem", What: what, Fix: fix})
	d.prose(ui.Bad(what))
	d.prose(ui.Fix(fix))
}

func (d *diagnosis) warn(what, fix string) {
	d.warns++
	d.checks = append(d.checks, doctorCheck{Level: "warning", What: what, Fix: fix})
	d.prose(ui.Warn(what))
	d.prose(ui.Fix(fix))
}

// prose is for the lines that exist only for a person: under --json they go
// nowhere, because stdout must stay one parseable document and none of them
// says anything the checks do not.
func (d *diagnosis) prose(line string) {
	if !d.json {
		fmt.Println(line)
	}
}

func (d *diagnosis) run(verbose bool) error {
	dir, inherited := paths.Resolve()

	// Through the shared vocabulary rather than raw escape codes. The inline
	// version wrote colour unconditionally, so `dibs doctor > report.txt`,
	// which is exactly what somebody does before opening an issue: produced a
	// file full of escape sequences, and NO_COLOR did nothing at all.
	ok, bad, warn := d.ok, d.bad, d.warn

	d.prose(ui.Bold("dibs doctor") + ui.Dim(": data dir "+ui.Path(dir)) + "\n")

	// Doctor exists to answer "why is it behaving like that", and reading a
	// directory the current version would not have created is high on the list.
	if inherited != "" {
		// One command, not a recipe.
		//
		// This used to hand back `mv`, plus a second sentence about re-running
		// `dibs configure --service` because a unit pins the old path as a
		// literal argument. Both are right and the ORDER is load-bearing: the
		// daemon has to be down for the move, and a unit rewritten before the
		// move points at a directory that does not exist yet. An operator
		// following two sentences in the wrong order ends up with a service that
		// starts against a path that is gone, which is how this hint's own
		// advice broke a machine.
		fix := "nothing is wrong and nothing is required. To adopt the current name, " +
			"`dibs upgrade --adopt-dir`, which stops the daemon, moves the directory, " +
			"repoints the service and starts it again in that order"
		if unit := unitPinning(inherited); unit != "" {
			fix += " (" + unit + " pins the old path, so a bare `mv` would leave the " +
				"service starting against a directory that is gone)"
		}
		warn("data directory is "+inherited+", named by an older version", fix)
	}

	// ── the daemon ───────────────────────────────────────────────────────
	secretPath := filepath.Join(dir, "local.secret")
	secret, err := os.ReadFile(secretPath) // #nosec G304 -- path derived from config, not input
	switch {
	case err != nil:
		bad("no local secret at "+secretPath,
			"the daemon has never run here. Start it: `dibd`")
		d.prose("\nnothing else can be checked until the daemon has started.")
		return earlyDoctorResult(d.probs, d.warns)
	default:
		ok("local secret present")
	}
	sec := strings.TrimSpace(string(secret))

	client := daemonClient(4 * time.Second)
	req, _ := http.NewRequest(http.MethodPost, origin()+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Dibs-Local", sec)
	resp, err := client.Do(req)
	if err != nil {
		bad("daemon unreachable at "+addr()+" ("+err.Error()+")",
			"start it with `dibd`, or set DIBS_ADDR if it listens elsewhere")
		// The LOCAL checks still run, and the ledger one most of all.
		//
		// A damaged ledger usually presents as exactly this: dibd refuses to
		// replay it and never starts listening. Returning here reported "daemon
		// unreachable" and stopped, so the one check that would have said WHY,
		// and told the operator not to delete the file, never ran in the case it
		// was written for. Everything below reads the data directory and needs no
		// daemon; the checks that need one stay above. Raised by the pre-release
		// review, which pointed out my tests called the helper directly and so
		// could not see that the command never reached it.
		d.prose("\nthe daemon is down, so nothing that needs it can be checked. What " +
			"follows reads this machine's own files, and is where the reason usually is.")
		checkLedgerAndBoard(dir, ok, bad, warn)
		checkGit(verbose, ok, bad)
		checkSupervision(verbose, ok, warn)
		checkOneDaemon(verbose, ok, warn)
		checkCodeSignature(ok, warn)
		checkServiceBinary(ok, warn)
		return earlyDoctorResult(d.probs, d.warns)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		bad("daemon rejected our own secret (401)",
			"the data dir was recreated under a running daemon. Restart `dibd`")
		return earlyDoctorResult(d.probs, d.warns)
	}
	ok("daemon answering on " + addr())

	var tl struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&tl) == nil {
		ok(fmt.Sprintf("%d tools published", len(tl.Result.Tools)))
		served := make(map[string]bool, len(tl.Result.Tools))
		for _, t := range tl.Result.Tools {
			served[t.Name] = true
		}
		checkPluginsMatchDaemon(client, sec, served, ok, warn)
	}

	checkHarnessConfigs(sec, addr(), ok, warn, bad)
	checkPanelBuild(client, sec, ok, warn, d.prose)
	checkMatching(client, sec, ok, warn)
	checkWakeRoutes(dir, ok, warn)
	checkHooks(client, sec, ok, bad, warn)
	checkLedgerAndBoard(dir, ok, bad, warn)
	checkGit(verbose, ok, bad)
	checkSupervision(verbose, ok, warn)
	checkOneDaemon(verbose, ok, warn)
	checkCodeSignature(ok, warn)
	checkServiceBinary(ok, warn)
	if b, err := boardSnapshot(); err == nil {
		checkCoordinatorIsReachable(b, ok, warn)
	}

	if d.json {
		// The tally is a rendering of counts the document already carries, and
		// the exit status is set the same way the early returns set it: the
		// document on stdout is the whole diagnosis.
		return earlyDoctorResult(d.probs, d.warns)
	}
	fmt.Println()
	switch {
	case d.probs > 0:
		fmt.Println(ui.Tally([]ui.Count{
			{Label: "problem(s)", N: d.probs, Tone: "alarm", Always: true},
			{Label: "warning(s)", N: d.warns, Tone: "attn"},
		}))
	case d.warns > 0:
		fmt.Println(ui.Good("no problems") + ui.Dim("; ") +
			ui.Attn(fmt.Sprintf("%d warning(s)", d.warns)) + ui.Dim(": all optional features"))
	default:
		fmt.Println(ui.Good("no problems found"))
	}
	return doctorResult(d.probs, d.warns)
}

// doctorResult is the machine-readable half of the report. Warnings describe
// optional features and are deliberately status-neutral; any problem means the
// install cannot be trusted as healthy by a script or monitoring probe.
func doctorResult(problems, _ int) error {
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	return nil
}

// earlyDoctorError tells main that doctor already printed the whole diagnosis.
// The status must fail, but repeating it as "dibs: N problem(s) found" would
// change the deliberately human-written early output for the sake of scripts.
type earlyDoctorError struct{ error }

func (earlyDoctorError) exitOnly() {}

func earlyDoctorResult(problems, warnings int) error {
	if err := doctorResult(problems, warnings); err != nil {
		return earlyDoctorError{error: err}
	}
	return nil
}

// secretPattern matches a Dibs local secret WHERE DIBS PUTS ONE: as the value
// of the X-Dibs-Local header, or as a bearer token. Used to tell "this config
// embeds a secret" from "this config uses the stdio bridge", which embeds none.
//
// SCOPED TO THE HEADER, not to any 64 hex characters in the file. A harness
// config holds every MCP server that harness has, and other people's servers
// carry their own credentials and hashes. This matched a bare 64-hex run
// anywhere in the file, so the SHA-256 in an unrelated server's
// NODE_REPL_TRUSTED_BROWSER_CLIENT_SHA256S was read as Dibs's secret, found not
// to be the current one, and reported as: "codex config has a STALE secret,
// that harness sees ZERO Dibs tools", against a codex install that was
// correctly configured on the stdio bridge and working.
//
// Two things make that worse than a wrong line. The advice is "run
// `dibs mcp-config` and re-copy the block", which would replace a working stdio
// configuration; and the comment on the branch below already records this
// exact class being fixed once, because a diagnostic that cries wolf is one
// people learn to ignore. Found in live use, against a real config.
var secretPattern = regexp.MustCompile(
	`(?i)(?:x-dibs-local["']?\s*[:=]\s*["']?|authorization["']?\s*[:=]\s*["']?bearer\s+)([0-9a-f]{64})`)

// embeddedSecrets returns the Dibs secrets a config carries, if any.
func embeddedSecrets(body string) []string {
	var out []string
	for _, m := range secretPattern.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// mcpURL matches the endpoint a harness config points a Dibs server at, so a
// config written for ANOTHER daemon can be told apart from a stale one.
var mcpURL = regexp.MustCompile(`https?://[^"'\s,}]+/mcp\b`)

// serverBlock matches the name a config gives an MCP server, in either
// supported layout: `[mcp_servers.dibs]` (TOML) or `"dibs": {` (JSON).
var serverBlock = regexp.MustCompile(`\[mcp[_a-zA-Z]*\.([A-Za-z0-9_.-]+)\]|"([A-Za-z0-9_-]+)"\s*:\s*\{`)

// structuralKeys are object keys that name a section of a server's config
// rather than a server. Without skipping them the nearest preceding name to a
// URL is whatever container it sits in.
var structuralKeys = map[string]bool{
	"mcpservers": true, "mcp_servers": true, "servers": true,
	"http_headers": true, "headers": true, "environment": true, "env": true,
}

// dibsTargets lists the MCP endpoints a config points DIBS at.
//
// Anchored on the server block that NAMES agents, because these configs hold
// many servers and listing all their URLs buries the one address the reader
// needs: the first version of this message named five Google and OpenAI
// endpoints first. Anchoring on the secret is not enough either: a config can
// hold several 64-hex strings and only one is ours, which is exactly the
// situation this branch exists for. Nor is a plain lookback window: a server
// block that FOLLOWS the agents block falls inside it, and an unrelated endpoint
// gets named as though it were the Dibs one.
func dibsTargets(body string) []string {
	names := serverBlock.FindAllStringSubmatchIndex(body, -1)
	var out []string
	seen := map[string]bool{}
	for _, u := range mcpURL.FindAllStringIndex(body, -1) {
		if !dibsOwns(body, names, u[0]) {
			continue
		}
		if url := body[u[0]:u[1]]; !seen[url] {
			seen[url] = true
			out = append(out, url)
		}
	}
	return out
}

// dibsOwns reports whether the server block containing position at is Dibs'.
func dibsOwns(body string, names [][]int, at int) bool {
	for i := len(names) - 1; i >= 0; i-- {
		m := names[i]
		if m[0] >= at {
			continue
		}
		name := ""
		for g := 1; g <= 2 && name == ""; g++ {
			if m[2*g] >= 0 {
				name = body[m[2*g]:m[2*g+1]]
			}
		}
		if structuralKeys[strings.ToLower(name)] {
			continue // a section of a server, not a server
		}
		return strings.Contains(strings.ToLower(name), "dibs")
	}
	return false
}

// targetsDaemon reports whether a config points Dibs at the daemon on addr.
//
// A config with no URL at all is assumed to be for this daemon: the stdio
// bridge and several harnesses take the address from the environment, and
// guessing "different daemon" there would trade one false alarm for another.
func targetsDaemon(body, addr string) bool {
	targets := dibsTargets(body)
	if len(targets) == 0 {
		return true
	}
	return slices.ContainsFunc(targets, func(u string) bool { return strings.Contains(u, addr) })
}

// The checks, split out so each reads as one idea and the whole thing stays
// under the complexity gate. Each takes the reporters rather than returning,
// because a doctor that stops at the first problem hides the rest.
type (
	reportFn func(string)
	fixFn    func(what, fix string)
)

func checkHarnessConfigs(sec, addr string, ok reportFn, warn, bad fixFn) {
	// A stale copy of the secret is the single most confusing failure Dibs has:
	// the harness starts fine, shows no error, and simply has zero Dibs tools.
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	for _, c := range []struct{ name, path string }{
		{"codex / chatgpt-desktop", filepath.Join(home, ".codex", "config.toml")},
		{"claude desktop", filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")},
	} {
		if seen[c.path] {
			continue
		}
		seen[c.path] = true
		body, err := os.ReadFile(c.path) // #nosec G304 -- fixed well-known paths
		if err != nil || !strings.Contains(string(body), "dibs") {
			continue
		}
		// Only a config that CARRIES a secret can carry a stale one.
		//
		// The stdio bridge (`command: agents, args: [mcp-stdio]`) reads the secret
		// from disk and embeds none, so "mentions agents but does not contain the
		// current secret" flags every stdio-configured harness as broken. That
		// false positive fired on the first real run of this tool, against a
		// perfectly healthy Claude Desktop config, and a diagnostic that cries
		// wolf is one people learn to ignore.
		found := embeddedSecrets(string(body))
		switch {
		case len(found) == 0:
			ok(c.name + " config uses the stdio bridge (no embedded secret to go stale)")
		case slices.Contains(found, sec):
			ok(c.name + " config carries the current secret")
		case !targetsDaemon(string(body), addr):
			// A config for ANOTHER daemon is not a stale config, and calling it
			// one is worse than saying nothing: the fix offered: re-copy the
			// block from `dibs mcp-config`: would repoint a working global
			// setup at whichever daemon doctor happened to be run against.
			// Anyone running a per-project daemon alongside their usual one sees
			// this, and the advice actively breaks them.
			warn(c.name+" config points at a different daemon ("+c.path+")",
				"its secret does not match this one because it is not for this "+
					"daemon: it names "+strings.Join(dibsTargets(string(body)), ", ")+
					" and you are checking "+addr+". Nothing to fix unless you meant "+
					"to point it here; re-run with DIBS_ADDR set to that daemon to check it")
		default:
			bad(c.name+" config has a STALE secret ("+c.path+")",
				"that harness sees ZERO Dibs tools and says nothing about it. "+
					"Run `dibs mcp-config` and re-copy the block")
		}
	}
}

// checkWakeRoutes says which way this board can actually reach a stopped agent.
//
// There are two, and they are not equals. `[wake.exec]` starts a process and
// the daemon sees its exit status, so a wake either happened or it did not.
// The session socket needs no configuration and is BEST EFFORT: the receiving
// harness decides whether to accept a peer message and sends no receipt, so a
// notice that is held looks exactly like one that was read.
//
// This check exists because the difference was invisible. Measured on the
// machine this is developed on: notices delivered to an idle live session,
// every write succeeding, and nothing arriving, because a Claude Code session
// in bypassPermissions mode holds peer messages for its human. The board
// reported healthy throughout. An operator running an unattended fleet is
// running exactly that mode, and should be told that the only route they have
// is the one that cannot be confirmed.
func checkWakeRoutes(dir string, ok reportFn, warn fixFn) {
	cfg, err := boardconfig.Load(dir)
	if err != nil {
		warn("cannot read the board configuration, so wake coverage is unknown",
			"fix "+filepath.Join(dir, "dibs.toml")+" and run this again: "+err.Error())
		return
	}
	if n := len(cfg.Wake.Exec); n > 0 {
		ok(fmt.Sprintf("%d wake command(s) configured: a wake either runs or reports why", n))
		return
	}
	warn("no wake command is configured, so the only route is best effort",
		"the harness session socket needs no setup and is tried first, but the "+
			"receiving session decides whether to accept a peer message and sends "+
			"no receipt: a Claude Code session in bypassPermissions mode HOLDS it "+
			"for its human, which is what an unattended fleet runs in. Nothing "+
			"will report a wake that was held. Add a [wake.exec.<harness>] block "+
			"to "+filepath.Join(dir, "dibs.toml")+" for a route this daemon can confirm")
}

func checkMatching(client *http.Client, sec string, ok reportFn, warn fixFn) {
	st := fetchMatchStatus(client, sec)
	switch st.Phase {
	case "off":
		warn("work-overlap matching has no repository indexed yet", st.Hint)
	case "indexing":
		warn("still indexing: declarations made now will not be matched", st.Hint)
	case "degraded":
		warn("matching degraded to the built-in scorer", st.Hint)
	case "suggest-only":
		// AS CONFIGURED, not a warning.
		//
		// join_threshold = 0 is the shipped default, it is documented, and it is
		// what this project recommends: auto-joining on a threshold nobody
		// measured is how every agent ends up in one space. Reporting the
		// recommended configuration as a `!` on every single run is the same
		// mistake MatchPhase exists to prevent, applied by the health check to
		// itself: a feature that is deliberately off must not look like a
		// feature that is broken. Reported by an operator seeing four warnings
		// every run, all of them "all optional features".
		ok("matching suggests and never joins, as configured (join_threshold = 0)")
	case "ready":
		// Name the repository, always.
		//
		// "matching ready (lexical+cochange, 4577 files, 1683 commits)" is a
		// confident line that says nothing about WHICH tree those files are in, and
		// the daemon indexes one repository for the whole machine. A board
		// configured for one project while somebody works in another reports
		// exactly this, then matches their declarations against a stranger's file
		// layout: a reviewer was shown another project's paths as evidence of
		// shared work and had no way, from any Dibs surface, to find out why.
		//
		// The counts made it worse by looking like health: four thousand files is
		// reassuring until you learn they are the wrong four thousand.
		where := ""
		if st.Repo != "" {
			where = " of " + st.Repo
		}
		ok(fmt.Sprintf("matching ready (%s, %d files, %d commits%s)",
			st.Scorer, st.Files, st.Commits, where))
		if st.Repo != "" {
			// And say so when that is not where this command was run. Matching is
			// machine-wide by design, one daemon, one index, so working elsewhere
			// is not an error. It is just the single fact that explains every
			// puzzling match, and nothing else surfaces it.
			if cwd, err := os.Getwd(); err == nil && !underDir(cwd, st.Repo) {
				warn("you are working in "+cwd+", which is not the indexed repository",
					"matching compares declarations against "+st.Repo+": work outside it "+
						"is matched against a stranger's file layout. Point [match] repo at "+
						"this tree in dibs.toml and restart, or expect suggestions to be "+
						"about the other project")
			}
		}
	default:
		warn("matching status unknown", "this daemon predates the check; restart `dibd`")
	}
}

// checkHooks answers the question no other check can: is the claim guard
// actually protecting anything?
//
// It fails open when it cannot resolve the caller, which is correct and makes a
// misconfigured guard indistinguishable from a board where nothing is claimed.
// The daemon sees every call and whether it resolved, so it is the only party
// that can tell, and this is the failure that cost a day.
func checkHooks(client *http.Client, sec string, ok reportFn, bad, warn fixFn) {
	var h struct {
		GuardResolved   int64  `json:"guard_resolved"`
		GuardUnresolved int64  `json:"guard_unresolved"`
		PollResolved    int64  `json:"poll_resolved"`
		PollUnresolved  int64  `json:"poll_unresolved"`
		Verdict         string `json:"verdict"`
		Hint            string `json:"hint"`
	}
	req, err := http.NewRequest(http.MethodGet, origin()+"/api/hook-health", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Dibs-Local", sec)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		warn("cannot read hook health", "this daemon predates the check; restart `dibd`")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return
	}
	switch h.Verdict {
	case "ok":
		ok(fmt.Sprintf("harness hooks resolving (%d guard, %d wake)", h.GuardResolved, h.PollResolved))
	case "never-called":
		warn("no harness has ever called this daemon's hooks", h.Hint)
	case "never-resolved", "guard-unresolved":
		// A problem, not a warning: the guard is running and protecting nothing.
		bad("hooks are reaching Dibs but resolving to NO agent: the guard is inert", h.Hint)
	case "poll-partly-unresolved":
		// A problem, not a warning. Mail that is never delivered is the failure
		// this product exists to prevent, and it was reading as a tick.
		bad(fmt.Sprintf("%d wake call(s) resolved to no agent: somebody's mail is not "+
			"being delivered", h.PollUnresolved), h.Hint)
	case "guard-mostly-unresolved":
		bad(fmt.Sprintf("%d of %d guard calls did not resolve to an agent",
			h.GuardUnresolved, h.GuardUnresolved+h.GuardResolved), h.Hint)
	}
}

func checkLedgerAndBoard(dir string, ok reportFn, bad, warn fixFn) {
	// A data directory that JOINS another machine's board holds a credential
	// and nothing else: the ledger is on the hub, and there is nothing here to
	// verify.
	//
	// Without this, following the documented recipe for a second machine ends
	// at `doctor` reporting "ledger does not verify ... do NOT delete it, open
	// an issue": a data-loss emergency raised against a completely healthy
	// join, aimed at the operator least able to tell it is spurious. Found by
	// following `mcp-config --board` end to end.
	//
	// node_id is the same test dibd itself uses ("this is not a board a daemon
	// has served"): it is written at first boot, before any op, so its absence
	// means no daemon has ever owned this directory. An empty board still has
	// one.
	// A ledger present with no node_id is NOT a join: it is a board that has
	// lost the file naming it, and skipping verification there would report the
	// one directory that most needs checking as healthy. The join case is a
	// credential and nothing else.
	if isJoinedBoard(dir) {
		ok("joined board: the ledger lives on the daemon serving it, not here")
		return
	}
	res, err := verifyChain(filepath.Join(dir, "ledger.jsonl"))
	switch {
	case err != nil:
		bad("ledger does not verify: "+err.Error(),
			"do NOT delete it. Copy it somewhere safe and open an issue. "+
				"the chain is the record of what every agent agreed")
	case res.Torn:
		// Not damage: a crash between write and fsync leaves a partial final
		// record, for an op that was never acknowledged. The daemon discards it
		// on replay. Worth showing (it says a daemon died mid-write) but
		// reporting it as a problem sends the operator hunting a breach they do
		// not have.
		ok(fmt.Sprintf("ledger chain intact (%d lines)", res.Lines))
		warn("the ledger's final record is incomplete",
			"a write interrupted by a crash or a kill, not damage. The op was never "+
				"acknowledged to the agent that sent it and the daemon discards the partial "+
				"record on its next replay: nothing to repair, but something did die mid-write")
	default:
		ok(fmt.Sprintf("ledger chain intact (%d lines)", res.Lines))
	}
	// Whether a notification would actually be SEEN, which is not the same as
	// whether one can be posted.
	//
	// Everything reported success while nothing appeared: a coordinator request
	// was posted, macOS accepted it, an active Focus swallowed the banner, and
	// the operator asked why they had seen nothing. This is the one path that
	// exists because the person is not in a loop to notice its absence, so it
	// must not be the one path that fails quietly.
	if reaches, why := notify.Reach(); reaches {
		ok("notifications reach you: a question or request from an agent raises one " +
			"with buttons on it")
	} else if why != "" {
		warn("agents cannot reach you by notification", why)
	}

	// Two ways in, and this used to report only one of them.
	//
	// "no admin password set, so the web board cannot be opened" was a warning
	// on every Mac with a working sensor, where the board opens on a fingerprint
	// and always could have. It sent the operator to invent and store a
	// credential in order to be trusted less than the fingerprint they already
	// had. A check that names a fault the machine does not have costs exactly
	// what the fault would have.
	_, pwErr := os.Stat(filepath.Join(dir, "admin.hash"))
	switch {
	case humanauth.Available():
		how := "`dibs web` unlocks the board with Touch ID"
		if pwErr == nil {
			how += ", and the admin password still works"
		} else {
			how += ". No admin password is needed here; set one only if you want a " +
				"way in that does not use the sensor"
		}
		ok(how)
	case pwErr != nil:
		warn("no way to open the web board: this machine cannot check presence and "+
			"no admin password is set",
			"run `dibs admin set-password`. Touch ID would do instead, on a machine "+
				"that has it")
	default:
		ok("admin password set. `dibs web` will open the board")
	}
}

// checkSupervision reports whether stall detection can actually attribute a
// spawned subagent to the agent that spawned it.
//
// Both halves fail SILENTLY when absent, which is why they are worth a check.
// Without `dibs` on PATH the PreToolUse hook cannot run, so nothing is
// stamped and attribution quietly falls back to inference. Without a readable
// process environment the stamp is written and never read. Neither produces an
// error anywhere; supervision simply reports fewer stalls, to fewer owners,
// and looks like it is working.
// checkPluginsMatchDaemon catches a plugin newer than the daemon it talks to.
//
// A shipped hook calls a tool by name. If the running daemon predates that tool
// (the normal state of an install upgraded halfway) every firing of that hook
// returns "unknown tool", visibly, at session start. Nothing is corrupted and
// nothing is blocked, but a person watching sees errors from software that is
// working as designed, which at a demo is indistinguishable from broken.
//
// The plugin files are the source of truth here rather than a hardcoded list,
// so a hook added later is checked without anybody remembering to update this.
//
// "Served" is decided by CALLING the tool, not by looking for it in tools/list.
// The two parted company when the harness lifecycle hooks stopped being
// advertised: they are deliberately absent from the listing and perfectly
// callable, so a listing-based check reported the daemon as older than its own
// plugins and told the reader to restart a daemon that was already current.
// Exactly the false alarm this function exists to prevent, produced by it.
// wantServer is the id a Claude Code plugin hook must address: plugin:<plugin>:<server>.
const wantServer = "plugin:dibs:dibs"

// codexServer is what a Codex hook names: the server directly, no plugin
// prefix, which is the convention of the harness that reads it.
const codexServer = "dibs"

// serverIDFor is the id a hook in THIS file has to address.
//
// The two shipped layouts are two harnesses: Claude Code reads
// `<plugin>/hooks/hooks.json` and addresses `plugin:<plugin>:<server>`; Codex
// reads a `hooks.json` at the root of its config directory and addresses the
// server directly. Comparing both against one spelling is how doctor came to
// report the correct Codex hook as broken.
func serverIDFor(file string) string {
	if filepath.Base(filepath.Dir(file)) == "hooks" {
		return wantServer
	}
	return codexServer
}

func checkPluginsMatchDaemon(c *http.Client, secret string, served map[string]bool, ok reportFn, warn fixFn) {
	wanted, misaddressed := scanShippedHooks()
	for id, file := range misaddressed {
		warn("a shipped hook is addressed to a server that does not exist",
			file+" names "+id+", but that plugin publishes "+serverIDFor(file)+". A hook "+
				"pointed at an unknown server never runs and reports nothing: mail is not "+
				"injected and the claim guard does not fire. Reinstall the plugin to pick "+
				"up the fix")
	}
	if len(wanted) == 0 {
		return // not running from a checkout; nothing to compare
	}
	var missing []string
	for tool := range wanted {
		if served[tool] || servesTool(c, secret, tool) {
			continue
		}
		missing = append(missing, tool)
	}
	if len(missing) == 0 {
		if len(misaddressed) == 0 {
			ok("shipped hooks name tools this daemon serves, on the server it publishes")
		}
		// A misaddressed hook has already been reported above, with its own
		// remedy. Falling through here printed a second warning listing the
		// tools this daemon does not serve, with the list empty, which reads as
		// a fault with no content and sends the reader looking for one.
		return
	}
	sort.Strings(missing)
	warn("the shipped hooks call tools this daemon does not serve: "+strings.Join(missing, ", "),
		"the daemon is older than the plugins. Every firing of those hooks returns "+
			"\"unknown tool\": harmless, but visible. Restart it with the current build: "+
			"`dibs stop && dibd &`")
}

// checkOneDaemon reports a fleet split across two boards.
//
// dibd refuses a second daemon by default, so reaching this state takes
// -allow-parallel or DIBS_ALLOW_PARALLEL, which is a legitimate thing to do
// for isolating agents you do not trust. It is also exactly how somebody ends up
// wondering why half their fleet is invisible, six hours later, having forgotten
// they set it. The guard prevents the accident; this names the deliberate case
// out loud, because the symptom is silence.
func checkOneDaemon(verbose bool, ok reportFn, warn fixFn) {
	running, err := paths.LiveDaemons()
	if err != nil {
		warn("cannot read the daemon registry",
			"so a fleet split across two boards would go unreported: "+err.Error())
		return
	}
	if len(running) <= 1 {
		if verbose {
			ok("one daemon on this machine: the whole fleet shares one board")
		}
		return
	}
	var where []string
	for _, d := range running {
		where = append(where, fmt.Sprintf("%s (pid %d, data %s)", d.Addr, d.PID, d.Dir))
	}
	warn(fmt.Sprintf("%d dibd daemons are running on this machine", len(running)),
		"each has its own board, so agents pointed at different ones cannot see each "+
			"other and every call still succeeds: "+strings.Join(where, "; ")+
			". If that is deliberate (SECURITY.md's isolation advice) nothing is wrong. "+
			"If not, stop all but one and point every harness at the survivor")
}

func checkSupervision(verbose bool, ok reportFn, warn fixFn) {
	// Qualified by harness, because the stamp is not universal and a doctor that
	// says otherwise is worse than one that says nothing. Only harnesses with a
	// hook Dibs can use WITHOUT spawning a subprocess stamp automatically; Codex
	// has none, so promising a Codex user that their subagents will be attributed
	// sends them looking for a mechanism that is not there when one is not.
	// Lineage still works everywhere: vouch_child, then register with the nonce.
	if _, err := exec.LookPath("dibs"); err != nil {
		warn("`dibs` is not on PATH",
			"where a harness supports it (Claude Code today), the PreToolUse hook that "+
				"stamps a spawned subagent with its parent runs `dibs hook-spawn`, so "+
				"without it on PATH nothing is stamped and stalled subagents are "+
				"attributed by inference or not at all")
	} else if verbose {
		ok("`dibs` on PATH: subagents are stamped where the harness has a usable hook " +
			"(Claude Code). Elsewhere, use vouch_child and register the child with the nonce")
	}

	// The session-path rung reads a directory layout that is Claude Desktop's
	// private business, not a documented interface. If it changes, that rung
	// stops matching and attribution quietly falls to a weaker one: no error,
	// no log, just fewer subagents attributed. So it is checked against a live
	// process rather than assumed, and reported as informational: the rung is a
	// FALLBACK below the deterministic stamp, so losing it is a degradation and
	// not a fault.
	if n := liveness.SessionPathRungWorking(); n > 0 {
		if verbose {
			ok(fmt.Sprintf("the session-path fallback still matches (%d process(es))", n))
		}
	} else if verbose {
		ok("no process is currently using the session-path fallback (nothing to check)")
	}

	// Read this process's own environment through the same path attribution
	// uses. A test binary is user-compiled, and so is `dibs`, so a failure
	// here means the mechanism is unavailable on this machine rather than
	// merely restricted for platform binaries.
	if !strings.Contains(liveness.EnvironOf(os.Getpid()), "PATH=") {
		warn("this machine does not expose process environments to `ps`",
			"stall reports will still work, but a stalled subagent will be attributed "+
				"to its parent by weaker signals: see SPEC-SUPERVISION.md §5")
	} else if verbose {
		ok("process environments are readable: subagent attribution is exact")
	}
}

func checkGit(verbose bool, ok reportFn, bad fixFn) {
	if _, err := exec.LookPath("git"); err != nil {
		bad("git is not on PATH", "matching mines co-change from git log and cannot work without it")
	} else if verbose {
		ok("git present")
	}
}

type matchStatusJSON struct {
	Phase   string `json:"phase"`
	Scorer  string `json:"scorer"`
	Files   int    `json:"files"`
	Commits int    `json:"commits"`
	// Repo is which tree those files came from. The daemon has always sent it and
	// this struct dropped it on the floor, so `doctor` reported four thousand
	// indexed files without ever saying whose, which is the one fact that
	// explains a matcher suggesting another project's paths.
	Repo string `json:"repo"`
	Hint string `json:"hint"`
}

// fetchMatchStatus asks the daemon why matching is or is not working. Failure
// to answer is itself an answer: an older daemon has no such endpoint.
func fetchMatchStatus(c *http.Client, secret string) matchStatusJSON {
	req, err := http.NewRequest(http.MethodGet, origin()+"/api/match-status", nil)
	if err != nil {
		return matchStatusJSON{}
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return matchStatusJSON{}
	}
	defer func() { _ = resp.Body.Close() }()
	var out matchStatusJSON
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// checkPanelBuild reports which board panel this daemon serves, and how to tell
// whether the client on screen is actually showing it.
//
// This exists because the panel's worst failure is a blank board, and a blank
// board has two completely different causes that look identical: the server is
// serving a panel that cannot fill, or the CLIENT is rendering an older panel it
// cached and no longer asks about. Measured on a real host: across three daemon
// restarts, and with the panel's URI deliberately changed so the tool result
// named a template it had never seen, that host issued zero resources/read. It
// fetches the panel once per client session and never again.
//
// So the daemon cannot fix a stale panel and (importantly) cannot DETECT one
// either. A client holding the current build and a client holding a stale one
// look the same from here; both simply stop asking. Guessing would flag every
// correctly-cached client, which is worse than saying nothing.
//
// What the daemon can do is publish the build it serves and name the one check
// that settles it. The panel prints its own build in its footer, so the two
// numbers either agree or they do not. That is why this is neither ok() nor
// warn() bait: it is a fact plus an instruction, printed whether or not anything
// is wrong, because the human reads doctor precisely when the panel looks wrong.
// The instruction rides on the prose reporter: under --json it is decoration
// (a standing note, not a finding) and stdout must stay one document.
func checkPanelBuild(c *http.Client, secret string, ok reportFn, warn fixFn, prose reportFn) {
	uri := fetchPanelURI(c, secret)
	if uri == "" {
		warn("could not read the board panel's resource",
			"the daemon is up but did not list a `ui://dibs/board/…` resource. "+
				"rebuild and reinstall with `task install`, then restart `dibd`")
		return
	}
	build := uri
	if i := strings.LastIndex(uri, "/"); i >= 0 {
		build = uri[i+1:]
	}
	ok("board panel served: build " + build)
	prose(ui.Fix("if the panel is blank or reads \"awaiting board\": look at the " +
		"build in its footer. Different, or no build line at all, means your client " +
		"cached an older panel: it fetches one per session, so restart the client. " +
		"Matching, and still blank, is a server-side fault worth reporting."))
}

// fetchPanelURI returns the versioned URI of the board panel template, which
// carries the hash of the template being served. Empty when it cannot be read.
func fetchPanelURI(c *http.Client, secret string) string {
	req, err := http.NewRequest(http.MethodPost, origin()+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{}}`))
	if err != nil {
		return ""
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result struct {
			Resources []struct {
				URI string `json:"uri"`
			} `json:"resources"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return ""
	}
	for _, r := range out.Result.Resources {
		if strings.HasPrefix(r.URI, "ui://dibs/board") {
			return r.URI
		}
	}
	return ""
}

// servesTool reports whether the daemon will DISPATCH a tool, listed or not.
//
// Deliberately called with empty arguments. Every tool worth probing here
// requires at least one, so the call is rejected by argument validation before
// it can do anything: the probe cannot register a session, fire a hook, or
// touch the ledger. Only a name the dispatcher does not know produces "unknown
// tool", which is precisely the distinction being drawn.
func servesTool(c *http.Client, secret, name string) bool {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` +
		name + `","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, origin()+"/mcp", strings.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := c.Do(req)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		// Unreachable is not the same as unserved, and saying "your daemon is
		// too old" because one probe timed out would be the false alarm again.
		return true
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return true
	}
	// Matching the PHRASE and the name separately, not a quoted form. The
	// refusal is a JSON error nested inside a content string, so the quotes
	// around the name arrive triple-escaped: a literal `unknown tool \"name\"`
	// looks right and matches nothing. A served tool's argument-validation error
	// names the tool too, but never says "unknown tool".
	refusal := string(raw)
	return !strings.Contains(refusal, "unknown tool") || !strings.Contains(refusal, name)
}

// underDir reports whether path is dir or inside it, after resolving symlinks so
// /tmp and /private/tmp do not read as different trees on macOS.
func underDir(path, dir string) bool {
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		rp = path
	}
	rd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		rd = dir
	}
	rel, err := filepath.Rel(rd, rp)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkCodeSignature reports an ad-hoc signed daemon on macOS.
//
// macOS keys a privacy grant to a program's code signature. The Go toolchain
// signs ad-hoc, which produces a fresh code-directory hash from every build, so
// the system treats each rebuild as a different program and any Files-and-
// Folders or Full Disk Access grant silently stops applying.
//
// That is invisible and expensive: matching indexes a checkout under ~/Desktop,
// works, and then stops after the next install, with the daemon reporting only
// that it cannot read the tree. Diagnosed here rather than left to be
// rediscovered, because the symptom points at the wrong thing every time.
func checkCodeSignature(ok reportFn, warn fixFn) {
	if runtime.GOOS != "darwin" {
		return
	}
	// The RUNNING daemon, not whatever is first on this shell's PATH.
	//
	// The launchd unit pins an absolute path, which need not be what LookPath
	// finds. Reporting on the wrong binary is worse than not reporting: after
	// installing a stable-identity build to one directory while the service
	// still runs an ad-hoc one from another, this said grants would survive when
	// for the daemon actually serving they would not. That misdiagnosis is the
	// exact thing this check exists to prevent.
	daemon := runningDaemonPath()
	if daemon == "" {
		var err error
		if daemon, err = exec.LookPath("dibd"); err != nil {
			return // a missing daemon is already reported by the checks above
		}
	}
	// #nosec G204 -- no shell: exec.Command passes argv directly, and daemon is
	// whatever exec.LookPath resolved on this PATH, never caller-supplied text.
	out, err := exec.Command("codesign", "-dv", "--verbose=2", daemon).CombinedOutput()
	if err != nil {
		return // nothing to say if codesign itself is unavailable
	}
	if !strings.Contains(string(out), "Signature=adhoc") {
		ok("dibd is signed with a stable identity, so privacy grants survive a rebuild")
		return
	}
	// An identity of DIBS' OWN, and named rather than chosen from a list.
	//
	// This used to say "pick one from `security find-identity`", which invites
	// borrowing a certificate some unrelated project on the machine owns. A
	// privacy grant keyed to somebody else's certificate is revoked the moment
	// they rotate or delete it, and the symptom is matching quietly going off:
	// the same invisible failure this check exists to catch, arrived at by
	// following its own advice. tools/signcheck carries the same rule.
	warn("dibd is ad-hoc signed, so a macOS privacy grant will not survive the next install",
		"only matters if your checkouts live under Desktop, Documents or Downloads, where "+
			"the daemon needs permission to read them. Give Dibs a signing identity of its "+
			"OWN, never one belonging to another project: Keychain Access → Certificate "+
			"Assistant → Create a Certificate, named \"Dibs Local Codesign\", type Code "+
			"Signing, self-signed, then reinstall with "+
			"DIBS_CODESIGN_IDENTITY=\"Dibs Local Codesign\" task install")
}

// runningDaemonPath is the executable behind the daemon serving this data
// directory, or "" if it cannot be determined.
func runningDaemonPath() string {
	daemons, err := paths.LiveDaemons()
	if err != nil {
		return ""
	}
	dir := paths.DataDir()
	for _, d := range daemons {
		if d.Unknown || d.IsStranger(dir) || d.PID <= 0 {
			continue
		}
		// #nosec G204 -- argv passed directly, no shell; the pid comes from this
		// user's own run registry and is formatted as a decimal integer.
		out, err := exec.Command("ps", "-p", strconv.Itoa(d.PID), "-o", "comm=").Output()
		if err != nil {
			return ""
		}
		if path := strings.TrimSpace(string(out)); path != "" {
			return path
		}
	}
	return ""
}

// checkServiceBinary reports a unit that starts a different daemon than the one
// installed.
//
// The unit pins an absolute path. Install the daemon somewhere else, or install
// it the first time from a Go workspace and later from `task install`, and the
// service keeps starting the old build indefinitely. Nothing reports it: the
// daemon answers, doctor says the daemon answers, and every fix shipped after
// that build is simply not running. Found on the machine this was written on,
// where the unit still pinned a dibd in ~/go/bin from an earlier install while
// every check above passed against a hand-started current one.
// checkCoordinatorIsReachable reports a coordinator nobody can become.
//
// The role can only be granted by the operator, and it is what force_release,
// close_space and clearing another agent's debris all key on. Held by an agent
// that registered with neither a nonce nor a session id, it is held by an
// identity no one can ever log back into, so the board has a coordinator on
// paper and nobody able to act as one. Nothing said so: the board shows the
// role, and the role looks filled.
//
// Not a deadlock, which is what it looks like from inside: `dibs admin
// coordinator <agent>` moves it, and a `[roles]` block in dibs.toml reapplies
// it on every start. The gap was that nothing pointed at either.
// boardSnapshot reads the public board for the checks that need it.
func boardSnapshot() (*boardView, error) {
	var b boardView
	if err := get("/api/board", &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func checkCoordinatorIsReachable(b *boardView, ok reportFn, warn fixFn) {
	var stuck []string
	live := ""
	for _, l := range b.Agents {
		if l.Status == "active" && live == "" {
			live = l.ID
		}
		if l.Role != "coordinator" {
			continue
		}
		// Reattachable agents are fine however dormant they are: their operator
		// comes back with the nonce and the role comes back with them.
		if l.Status != "active" && l.Unreachable {
			stuck = append(stuck, l.ID)
		}
	}
	if len(stuck) == 0 {
		return
	}
	if live == "" {
		live = "<a live agent>"
	}
	warn("the coordinator is an agent nobody can become: "+strings.Join(stuck, ", "),
		"it registered with neither a nonce nor a session id, so no one can reattach to it, "+
			"and force_release, close_space and clearing another agent's debris all need a "+
			"coordinator. Move the role to an agent that is here: `dibs admin coordinator "+
			live+"`, or declare it in [roles] in dibs.toml so it survives a reset. Its mail "+
			"is recovered separately, with adopt_agent")
	_ = ok
}

func checkServiceBinary(ok reportFn, warn fixFn) {
	unit, pinned := unitDaemon()
	if unit == "" || pinned == "" {
		return // no service installed: `configure --service` is optional
	}
	want, err := daemonPath()
	if err != nil {
		return // already reported by the checks above
	}
	if sameBinary(pinned, want) {
		ok("the service starts the dibd you have installed")
		return
	}
	detail := "it pins " + pinned + ", but the dibd you have installed is " + want
	if _, err := os.Stat(pinned); err != nil {
		detail = "it pins " + pinned + ", which does not exist; the dibd you have installed is " + want
	}
	warn("the service would start a different daemon than the one installed",
		detail+". Every fix since that build is not running when the service starts it. "+
			"`dibs upgrade` rewrites the unit and restarts onto the installed daemon, "+
			"after checking that it can rebuild this board")
}

// sameBinary compares two paths by inode where it can, so a symlink or a
// ./bin/../bin spelling is not reported as a mismatch.
func sameBinary(a, b string) bool {
	sa, erra := os.Stat(a)
	sb, errb := os.Stat(b)
	if erra == nil && errb == nil {
		return os.SameFile(sa, sb)
	}
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	return ea != "" && ea == eb
}

// scanShippedHooks reads the hook payloads this checkout (or install) ships and
// reports what they ask of the daemon: the tools they name, and any server id
// that is not the one this plugin publishes.
//
// Split out from the check so the check reads as its decision rather than as a
// file walk, and so the two questions it asks stay visibly separate: a hook can
// name a tool that exists and still be addressed to a server that does not.
func scanShippedHooks() (wanted, misaddressed map[string]string) {
	wanted, misaddressed = map[string]string{}, map[string]string{}
	roots := []string{
		filepath.Join(paths.DataDir(), "..", ".claude", "plugins"), // unusual, but cheap to try
		"plugins",
	}
	tool := regexp.MustCompile(`"tool"\s*:\s*"([a-z_]+)"`)
	server := regexp.MustCompile(`"server"\s*:\s*"([^"]+)"`)
	for _, root := range roots {
		// BOTH LAYOUTS, for the reason internal/mcp/required_test.go carries at
		// length: Codex reads a hooks.json at the root of its config directory,
		// not the nested Claude Code path, so scanning only the nested one left
		// the Codex plugin's hooks unexamined while doctor printed the all-clear.
		var matches []string
		for _, pat := range [][]string{
			{root, "*", "hooks", "hooks.json"},
			{root, "*", "hooks.json"},
		} {
			found, _ := filepath.Glob(filepath.Join(pat...))
			matches = append(matches, found...)
		}
		for _, f := range matches {
			raw, err := os.ReadFile(f) // #nosec G304 -- a repo path from a glob
			if err != nil {
				continue
			}
			for _, m := range tool.FindAllStringSubmatch(string(raw), -1) {
				wanted[m[1]] = f
			}
			// The tool name is only half of a hook's address. Every hook in the
			// Claude Code plugin named server `plugin:agents:agents`, which has
			// not existed since the rename, and the check passed the whole time:
			// the tools it asks for ARE served, they were simply addressed to
			// nothing. A hook whose server is unknown does not fail loudly, it
			// never runs, so mail injection and the claim guard were off with a
			// tick beside them. Found when a coordinator holding two unread
			// messages, one a request with a deadline, was never woken.
			// THE EXPECTED ID DEPENDS ON THE LAYOUT, because the two harnesses
			// address servers differently. A Claude Code plugin hook names
			// `plugin:<plugin>:<server>`; Codex names the server directly. When
			// this scanner learned to read both layouts it kept comparing every
			// file against the Claude Code spelling, so `dibs doctor` reported
			// the correct shipped Codex hook as addressed to a server that does
			// not exist, and told the operator to reinstall the plugin, which
			// cannot fix a file that is already right.
			want := serverIDFor(f)
			for _, m := range server.FindAllStringSubmatch(string(raw), -1) {
				if m[1] != want {
					misaddressed[m[1]] = f
				}
			}
		}
	}
	return wanted, misaddressed
}

// isJoinedBoard reports whether this data directory holds a credential for
// SOMEBODY ELSE'S board and nothing of its own.
//
// "Credential and nothing else" is what the join case actually is, and keying
// it on a missing node_id and ledger did not establish that: a local board that
// lost both, but still holds the key it encrypts with and the blobs it wrote,
// was reported as a healthy join. That directory has lost its replayable state,
// which is the one thing this check exists to notice, and it was told nothing
// was wrong. Raised by the pre-release review.
func isJoinedBoard(dir string) bool {
	if !fileExists(filepath.Join(dir, "local.secret")) {
		return false
	}
	// Anything a daemon writes for a board of its own. Any of them present and
	// this directory is a board, however damaged.
	// tls-key.pem and admin.hash are here for the same reason as the rest: a
	// joining client may hold the board's public CERTIFICATE, but it never has
	// the private key, and it never has the board's admin hash. Both are
	// unambiguously daemon-owned, and a local board that had lost its ledger,
	// node id, encryption key and blobs while keeping one of them read as a
	// healthy join, which skipped the very check that would have reported the
	// loss. Raised by the pre-release review.
	for _, own := range []string{
		"node_id", "ledger.jsonl", "key", "blobs", "coordinator.claim", "out",
		"tls-key.pem", "admin.hash",
		// dibs.toml is what `dibs configure` writes for a board of ITS OWN, and
		// a joining directory has no daemon to configure. Without it, the
		// ordinary output of the wizard, local.secret beside dibs.toml, read as
		// a join the moment the ledger went missing: exactly the configured
		// board whose loss most deserves reporting.
		"dibs.toml",
	} {
		if _, err := os.Stat(filepath.Join(dir, own)); err == nil {
			return false
		}
	}
	return true
}
