package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/lanes/internal/liveness"
	"github.com/agenxy/lanes/internal/paths"
	"github.com/agenxy/lanes/internal/ui"
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
// stuck; "the secret in ~/.codex/config.toml no longer matches — run `lanes
// mcp-config` and re-copy it" does not.
func doctor(args []string) error {
	verbose := len(args) > 0 && (args[0] == "-v" || args[0] == "--verbose")
	dir := paths.DataDir()
	var probs, warns int

	// Through the shared vocabulary rather than raw escape codes. The inline
	// version wrote colour unconditionally, so `lanes doctor > report.txt` —
	// which is exactly what somebody does before opening an issue — produced a
	// file full of escape sequences, and NO_COLOR did nothing at all.
	ok := func(what string) { fmt.Println(ui.OK(what)) }
	bad := func(what, fix string) {
		probs++
		fmt.Println(ui.Bad(what))
		fmt.Println(ui.Fix(fix))
	}
	warn := func(what, fix string) {
		warns++
		fmt.Println(ui.Warn(what))
		fmt.Println(ui.Fix(fix))
	}

	fmt.Println(ui.Bold("lanes doctor") + ui.Dim(" — data dir "+ui.Path(dir)) + "\n")

	// ── the daemon ───────────────────────────────────────────────────────
	secretPath := filepath.Join(dir, "local.secret")
	secret, err := os.ReadFile(secretPath) // #nosec G304 -- path derived from config, not input
	switch {
	case err != nil:
		bad("no local secret at "+secretPath,
			"the daemon has never run here. Start it: `lanesd`")
		fmt.Println("\nnothing else can be checked until the daemon has started.")
		return earlyDoctorResult(probs, warns)
	default:
		ok("local secret present")
	}
	sec := strings.TrimSpace(string(secret))

	client := &http.Client{Timeout: 4 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr()+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Lanes-Local", sec)
	resp, err := client.Do(req)
	if err != nil {
		bad("daemon unreachable at "+addr()+" ("+err.Error()+")",
			"start it with `lanesd`, or set LANES_ADDR if it listens elsewhere")
		fmt.Println("\nnothing else can be checked while the daemon is down.")
		return earlyDoctorResult(probs, warns)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		bad("daemon rejected our own secret (401)",
			"the data dir was recreated under a running daemon. Restart `lanesd`")
		return earlyDoctorResult(probs, warns)
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
	checkPanelBuild(client, sec, ok, warn)
	checkMatching(client, sec, ok, warn)
	checkHooks(client, sec, ok, bad, warn)
	checkLedgerAndBoard(dir, ok, bad, warn)
	checkGit(verbose, ok, bad)
	checkSupervision(verbose, ok, warn)
	checkOneDaemon(verbose, ok, warn)

	fmt.Println()
	switch {
	case probs > 0:
		fmt.Println(ui.Tally([]ui.Count{
			{Label: "problem(s)", N: probs, Tone: "alarm", Always: true},
			{Label: "warning(s)", N: warns, Tone: "attn"},
		}))
	case warns > 0:
		fmt.Println(ui.Good("no problems") + ui.Dim("; ") +
			ui.Attn(fmt.Sprintf("%d warning(s)", warns)) + ui.Dim(" — all optional features"))
	default:
		fmt.Println(ui.Good("no problems found"))
	}
	return doctorResult(probs, warns)
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
// The status must fail, but repeating it as "lanes: N problem(s) found" would
// change the deliberately human-written early output for the sake of scripts.
type earlyDoctorError struct{ error }

func (earlyDoctorError) exitOnly() {}

func earlyDoctorResult(problems, warnings int) error {
	if err := doctorResult(problems, warnings); err != nil {
		return earlyDoctorError{error: err}
	}
	return nil
}

// secretPattern matches a Lanes local secret as it appears in a harness config:
// 64 hex characters. Used to tell "this config embeds a secret" from "this
// config uses the stdio bridge", which embeds none.
var secretPattern = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

// mcpURL matches the endpoint a harness config points a Lanes server at, so a
// config written for ANOTHER daemon can be told apart from a stale one.
var mcpURL = regexp.MustCompile(`https?://[^"'\s,}]+/mcp\b`)

// serverBlock matches the name a config gives an MCP server, in either
// supported layout: `[mcp_servers.lanes]` (TOML) or `"lanes": {` (JSON).
var serverBlock = regexp.MustCompile(`\[mcp[_a-zA-Z]*\.([A-Za-z0-9_.-]+)\]|"([A-Za-z0-9_-]+)"\s*:\s*\{`)

// structuralKeys are object keys that name a section of a server's config
// rather than a server. Without skipping them the nearest preceding name to a
// URL is whatever container it sits in.
var structuralKeys = map[string]bool{
	"mcpservers": true, "mcp_servers": true, "servers": true,
	"http_headers": true, "headers": true, "environment": true, "env": true,
}

// lanesTargets lists the MCP endpoints a config points LANES at.
//
// Anchored on the server block that NAMES lanes, because these configs hold
// many servers and listing all their URLs buries the one address the reader
// needs — the first version of this message named five Google and OpenAI
// endpoints first. Anchoring on the secret is not enough either: a config can
// hold several 64-hex strings and only one is ours, which is exactly the
// situation this branch exists for. Nor is a plain lookback window: a server
// block that FOLLOWS the lanes block falls inside it, and an unrelated endpoint
// gets named as though it were the Lanes one.
func lanesTargets(body string) []string {
	names := serverBlock.FindAllStringSubmatchIndex(body, -1)
	var out []string
	seen := map[string]bool{}
	for _, u := range mcpURL.FindAllStringIndex(body, -1) {
		if !lanesOwns(body, names, u[0]) {
			continue
		}
		if url := body[u[0]:u[1]]; !seen[url] {
			seen[url] = true
			out = append(out, url)
		}
	}
	return out
}

// lanesOwns reports whether the server block containing position at is Lanes'.
func lanesOwns(body string, names [][]int, at int) bool {
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
		return strings.Contains(strings.ToLower(name), "lanes")
	}
	return false
}

// targetsDaemon reports whether a config points Lanes at the daemon on addr.
//
// A config with no URL at all is assumed to be for this daemon: the stdio
// bridge and several harnesses take the address from the environment, and
// guessing "different daemon" there would trade one false alarm for another.
func targetsDaemon(body, addr string) bool {
	targets := lanesTargets(body)
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
	// A stale copy of the secret is the single most confusing failure Lanes has:
	// the harness starts fine, shows no error, and simply has zero Lanes tools.
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
		if err != nil || !strings.Contains(string(body), "lanes") {
			continue
		}
		// Only a config that CARRIES a secret can carry a stale one.
		//
		// The stdio bridge (`command: lanes, args: [mcp-stdio]`) reads the secret
		// from disk and embeds none, so "mentions lanes but does not contain the
		// current secret" flags every stdio-configured harness as broken. That
		// false positive fired on the first real run of this tool, against a
		// perfectly healthy Claude Desktop config — and a diagnostic that cries
		// wolf is one people learn to ignore.
		found := secretPattern.FindAllString(string(body), -1)
		switch {
		case len(found) == 0:
			ok(c.name + " config uses the stdio bridge (no embedded secret to go stale)")
		case slices.Contains(found, sec):
			ok(c.name + " config carries the current secret")
		case !targetsDaemon(string(body), addr):
			// A config for ANOTHER daemon is not a stale config, and calling it
			// one is worse than saying nothing: the fix offered — re-copy the
			// block from `lanes mcp-config` — would repoint a working global
			// setup at whichever daemon doctor happened to be run against.
			// Anyone running a per-project daemon alongside their usual one sees
			// this, and the advice actively breaks them.
			warn(c.name+" config points at a different daemon ("+c.path+")",
				"its secret does not match this one because it is not for this "+
					"daemon — it names "+strings.Join(lanesTargets(string(body)), ", ")+
					" and you are checking "+addr+". Nothing to fix unless you meant "+
					"to point it here; re-run with LANES_ADDR set to that daemon to check it")
		default:
			bad(c.name+" config has a STALE secret ("+c.path+")",
				"that harness sees ZERO Lanes tools and says nothing about it. "+
					"Run `lanes mcp-config` and re-copy the block")
		}
	}
}

func checkMatching(client *http.Client, sec string, ok reportFn, warn fixFn) {
	st := fetchMatchStatus(client, sec)
	switch st.Phase {
	case "off":
		warn("work-overlap matching is off", st.Hint)
	case "indexing":
		warn("still indexing — declarations made now will not be matched", st.Hint)
	case "degraded":
		warn("matching degraded to the built-in scorer", st.Hint)
	case "suggest-only":
		warn("matching suggests but never joins", st.Hint)
	case "ready":
		// Name the repository, always.
		//
		// "matching ready (lexical+cochange, 4577 files, 1683 commits)" is a
		// confident line that says nothing about WHICH tree those files are in, and
		// the daemon indexes one repository for the whole machine. A board
		// configured for one project while somebody works in another reports
		// exactly this, then matches their declarations against a stranger's file
		// layout — a reviewer was shown another project's paths as evidence of
		// shared work and had no way, from any Lanes surface, to find out why.
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
			// machine-wide by design — one daemon, one index — so working elsewhere
			// is not an error. It is just the single fact that explains every
			// puzzling match, and nothing else surfaces it.
			if cwd, err := os.Getwd(); err == nil && !underDir(cwd, st.Repo) {
				warn("you are working in "+cwd+", which is not the indexed repository",
					"matching compares declarations against "+st.Repo+" — work outside it "+
						"is matched against a stranger's file layout. Point [match] repo at "+
						"this tree in lanes.toml and restart, or expect suggestions to be "+
						"about the other project")
			}
		}
	default:
		warn("matching status unknown", "this daemon predates the check; restart `lanesd`")
	}
}

// checkHooks answers the question no other check can: is the claim guard
// actually protecting anything?
//
// It fails open when it cannot resolve the caller, which is correct and makes a
// misconfigured guard indistinguishable from a board where nothing is claimed.
// The daemon sees every call and whether it resolved, so it is the only party
// that can tell — and this is the failure that cost a day.
func checkHooks(client *http.Client, sec string, ok reportFn, bad, warn fixFn) {
	var h struct {
		GuardResolved   int64  `json:"guard_resolved"`
		GuardUnresolved int64  `json:"guard_unresolved"`
		PollResolved    int64  `json:"poll_resolved"`
		Verdict         string `json:"verdict"`
		Hint            string `json:"hint"`
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr()+"/api/hook-health", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Lanes-Local", sec)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		warn("cannot read hook health", "this daemon predates the check; restart `lanesd`")
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
		bad("hooks are reaching Lanes but resolving to NO agent — the guard is inert", h.Hint)
	case "guard-mostly-unresolved":
		bad(fmt.Sprintf("%d of %d guard calls did not resolve to an agent",
			h.GuardUnresolved, h.GuardUnresolved+h.GuardResolved), h.Hint)
	}
}

func checkLedgerAndBoard(dir string, ok reportFn, bad, warn fixFn) {
	res, err := verifyChain(filepath.Join(dir, "ledger.jsonl"))
	switch {
	case err != nil:
		bad("ledger does not verify: "+err.Error(),
			"do NOT delete it. Copy it somewhere safe and open an issue — "+
				"the chain is the record of what every agent agreed")
	case res.Torn:
		// Not damage: a crash between write and fsync leaves a partial final
		// record, for an op that was never acknowledged. The daemon discards it
		// on replay. Worth showing — it says a daemon died mid-write — but
		// reporting it as a problem sends the operator hunting a breach they do
		// not have.
		ok(fmt.Sprintf("ledger chain intact (%d lines)", res.Lines))
		warn("the ledger's final record is incomplete",
			"a write interrupted by a crash or a kill, not damage. The op was never "+
				"acknowledged to the agent that sent it and the daemon discards the partial "+
				"record on its next replay — nothing to repair, but something did die mid-write")
	default:
		ok(fmt.Sprintf("ledger chain intact (%d lines)", res.Lines))
	}
	if _, err := os.Stat(filepath.Join(dir, "admin.hash")); err != nil {
		warn("no admin password set, so the web board cannot be opened",
			"run `lanes admin set-password`")
	} else {
		ok("admin password set — `lanes web` will open the board")
	}
}

// checkSupervision reports whether stall detection can actually attribute a
// spawned subagent to the agent that spawned it.
//
// Both halves fail SILENTLY when absent, which is why they are worth a check.
// Without `lanes` on PATH the PreToolUse hook cannot run, so nothing is
// stamped and attribution quietly falls back to inference. Without a readable
// process environment the stamp is written and never read. Neither produces an
// error anywhere; supervision simply reports fewer stalls, to fewer owners,
// and looks like it is working.
// checkPluginsMatchDaemon catches a plugin newer than the daemon it talks to.
//
// A shipped hook calls a tool by name. If the running daemon predates that tool
// — the normal state of an install upgraded halfway — every firing of that hook
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
func checkPluginsMatchDaemon(c *http.Client, secret string, served map[string]bool, ok reportFn, warn fixFn) {
	roots := []string{
		filepath.Join(paths.DataDir(), "..", ".claude", "plugins"), // unusual, but cheap to try
		"plugins",
	}
	wanted := map[string]string{} // tool -> the file that asks for it
	for _, root := range roots {
		matches, _ := filepath.Glob(filepath.Join(root, "*", "hooks", "hooks.json"))
		for _, f := range matches {
			raw, err := os.ReadFile(f) // #nosec G304 -- a repo path from a glob
			if err != nil {
				continue
			}
			for _, m := range regexp.MustCompile(`"tool"\s*:\s*"([a-z_]+)"`).FindAllStringSubmatch(string(raw), -1) {
				wanted[m[1]] = f
			}
		}
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
		ok("shipped hooks match the running daemon")
		return
	}
	sort.Strings(missing)
	warn("the shipped hooks call tools this daemon does not serve: "+strings.Join(missing, ", "),
		"the daemon is older than the plugins. Every firing of those hooks returns "+
			"\"unknown tool\" — harmless, but visible. Restart it with the current build: "+
			"`pkill lanesd && lanesd &`")
}

// checkOneDaemon reports a fleet split across two boards.
//
// lanesd refuses a second daemon by default, so reaching this state takes
// -allow-parallel or LANES_ALLOW_PARALLEL — which is a legitimate thing to do
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
			ok("one daemon on this machine — the whole fleet shares one board")
		}
		return
	}
	var where []string
	for _, d := range running {
		where = append(where, fmt.Sprintf("%s (pid %d, data %s)", d.Addr, d.PID, d.Dir))
	}
	warn(fmt.Sprintf("%d lanesd daemons are running on this machine", len(running)),
		"each has its own board, so agents pointed at different ones cannot see each "+
			"other and every call still succeeds: "+strings.Join(where, "; ")+
			". If that is deliberate (SECURITY.md's isolation advice) nothing is wrong. "+
			"If not, stop all but one and point every harness at the survivor")
}

func checkSupervision(verbose bool, ok reportFn, warn fixFn) {
	if _, err := exec.LookPath("lanes"); err != nil {
		warn("`lanes` is not on PATH",
			"the PreToolUse hook that stamps a spawned subagent with its parent runs "+
				"`lanes hook-spawn`, so without it on PATH nothing is stamped and stalled "+
				"subagents are attributed by inference or not at all")
	} else if verbose {
		ok("`lanes` on PATH — spawned subagents will be stamped with their parent")
	}

	// The session-path rung reads a directory layout that is Claude Desktop's
	// private business, not a documented interface. If it changes, that rung
	// stops matching and attribution quietly falls to a weaker one — no error,
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
	// uses. A test binary is user-compiled, and so is `lanes`, so a failure
	// here means the mechanism is unavailable on this machine rather than
	// merely restricted for platform binaries.
	if !strings.Contains(liveness.EnvironOf(os.Getpid()), "PATH=") {
		warn("this machine does not expose process environments to `ps`",
			"stall reports will still work, but a stalled subagent will be attributed "+
				"to its parent by weaker signals — see SPEC-SUPERVISION.md §5")
	} else if verbose {
		ok("process environments are readable — subagent attribution is exact")
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
	// indexed files without ever saying whose — which is the one fact that
	// explains a matcher suggesting another project's paths.
	Repo string `json:"repo"`
	Hint string `json:"hint"`
}

// fetchMatchStatus asks the daemon why matching is or is not working. Failure
// to answer is itself an answer: an older daemon has no such endpoint.
func fetchMatchStatus(c *http.Client, secret string) matchStatusJSON {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr()+"/api/match-status", nil)
	if err != nil {
		return matchStatusJSON{}
	}
	req.Header.Set("X-Lanes-Local", secret)
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
// So the daemon cannot fix a stale panel and — importantly — cannot DETECT one
// either. A client holding the current build and a client holding a stale one
// look the same from here; both simply stop asking. Guessing would flag every
// correctly-cached client, which is worse than saying nothing.
//
// What the daemon can do is publish the build it serves and name the one check
// that settles it. The panel prints its own build in its footer, so the two
// numbers either agree or they do not. That is why this is neither ok() nor
// warn() bait: it is a fact plus an instruction, printed whether or not anything
// is wrong, because the human reads doctor precisely when the panel looks wrong.
func checkPanelBuild(c *http.Client, secret string, ok reportFn, warn fixFn) {
	uri := fetchPanelURI(c, secret)
	if uri == "" {
		warn("could not read the board panel's resource",
			"the daemon is up but did not list a `ui://lanes/board/…` resource — "+
				"rebuild and reinstall with `task install`, then restart `lanesd`")
		return
	}
	build := uri
	if i := strings.LastIndex(uri, "/"); i >= 0 {
		build = uri[i+1:]
	}
	ok("board panel served: build " + build)
	fmt.Println(ui.Fix("if the panel is blank or reads \"awaiting board\": look at the " +
		"build in its footer. Different, or no build line at all, means your client " +
		"cached an older panel — it fetches one per session, so restart the client. " +
		"Matching, and still blank, is a server-side fault worth reporting."))
}

// fetchPanelURI returns the versioned URI of the board panel template, which
// carries the hash of the template being served. Empty when it cannot be read.
func fetchPanelURI(c *http.Client, secret string) string {
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{}}`))
	if err != nil {
		return ""
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Lanes-Local", secret)
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
		if strings.HasPrefix(r.URI, "ui://lanes/board") {
			return r.URI
		}
	}
	return ""
}

// servesTool reports whether the daemon will DISPATCH a tool, listed or not.
//
// Deliberately called with empty arguments. Every tool worth probing here
// requires at least one, so the call is rejected by argument validation before
// it can do anything — the probe cannot register a session, fire a hook, or
// touch the ledger. Only a name the dispatcher does not know produces "unknown
// tool", which is precisely the distinction being drawn.
func servesTool(c *http.Client, secret, name string) bool {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` +
		name + `","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/mcp", strings.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Lanes-Local", secret)
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
	// around the name arrive triple-escaped — a literal `unknown tool \"name\"`
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
