// Package plugins hands an agent the Dibs plugin for its own harness, over MCP.
//
// Dibs works with no plugin at all: the daemon is the product and every tool
// behaves the same without one. What a plugin buys is delivery: on Claude Code
// it turns mail from something an agent must remember to poll for into something
// that arrives in the session, which is the difference between a board that gets
// read and one that gets forgotten.
//
// The problem was never that agents refused to install it. It was that nothing
// ever told them it existed. An agent connects, sees forty tools, and has no way
// to learn that this particular harness has a hook that would wake it: the
// information lived in a README in a repository the agent may not have, and
// nobody reads a repository they did not clone.
//
// So the server carries the plugin itself. Not a link to it, not an install
// command that assumes network access and a checkout: the actual bytes, over the
// same connection the agent already trusts, so an agent that decides to install
// can write the files and be done. A pointer would have reintroduced exactly the
// gap this closes, because the reason the plugin went uninstalled was never
// unwillingness: it was distance.
//
// The files are duplicated under data/ because go:embed cannot reach above its
// own package, and plugins/ at the repository root stays canonical. Drift
// between the two is a defect, and drift_test.go fails on it: the same
// arrangement skills.md already uses for the same reason.
package plugins

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// all: is load-bearing. A bare `go:embed data` silently skips every path whose
// name begins with a dot, so .claude-plugin/plugin.json and .mcp.json: the
// manifest and the MCP server definition, without which the thing is not a
// plugin at all: were omitted from a payload documented as "the whole plugin".
// No error, no warning: the directive simply walked past them.
//
//go:embed all:data
var files embed.FS

// Plugin is everything an agent needs to install one, in one object.
type Plugin struct {
	// Harness is the canonical name, matching what agents report in register.
	Harness string `json:"harness"`
	// Buys says what installing it actually changes, in the agent's terms. Not a
	// feature list: an agent deciding whether to spend a turn on this needs to
	// know what it gets, and "adds hooks" is not that.
	Buys string `json:"buys"`
	// Install is the one command, where a command exists.
	Install string `json:"install,omitempty"`
	// Files maps a path relative to the plugin root to its contents, so an agent
	// with no network and no checkout can still write the plugin out. Every file
	// the canonical plugin has, including dotfiles: an install missing its
	// manifest is not an install.
	Files map[string]string `json:"files"`
	// Root is where Files are conventionally written on this harness.
	Root string `json:"root"`
	// Setup is the ordered procedure, and every step carries its own check.
	//
	// Instructions without verification are how a half-configured harness looks
	// identical to a working one: the agent writes a hooks file, reports success,
	// and nothing ever fires. Each step therefore says what to DO and how to know
	// it actually took effect.
	Setup []Step `json:"setup"`
	// Verify is the single end-to-end check that the whole thing works, phrased
	// so a wrong answer is unambiguous.
	Verify string `json:"verify"`
	// Delivers says whether this harness has a WAKE PATH: whether mail can reach
	// the agent without it asking.
	//
	// Not every harness with lifecycle hooks has one. Codex fires hooks as
	// subprocesses, which Dibs refuses to be, so it has hook traffic and no
	// delivery: an agent there must still pull. Without this flag the two facts
	// were reported by different code paths and contradicted each other in the
	// same result. "mail will arrive, you do not need to poll" beside an entry
	// stating mail is pull-only. An agent that believed the first would stop
	// checking and silently lose mail.
	Delivers bool `json:"delivers"`
}

// Step is one action and the evidence that it worked.
type Step struct {
	Do     string `json:"do"`
	Check  string `json:"check"`
	IfNot  string `json:"if_not,omitempty"`
	Manual bool   `json:"needs_human,omitempty"`
}

// catalog is the per-harness metadata. The FILES come from the embed; only the
// prose and the paths live here, so adding a file to a plugin never means
// editing this table.
var catalog = []struct {
	harness, dir, buys, install, root, verify string
	aliases                                   []string
	setup                                     []Step
	delivers                                  bool
}{
	{
		harness: "claude-code",
		dir:     "claude-code",
		aliases: []string{"claude", "claudecode", "claude_code"},
		// What the SHIPPED hooks actually do.
		//
		// This said a PreToolUse hook calls the wake path, so mail "appears in
		// your context on your next tool call". It does not: hooks.json binds
		// hook_poll to SessionStart, UserPromptSubmit, Stop and SubagentStop,
		// and PreToolUse to the claim guard alone. Mail therefore arrives at a
		// turn boundary, and an agent working a long autonomous stretch has no
		// boundary until it finishes.
		//
		// That mattered. A peer sent a question with the default 600-second
		// deadline to an agent seven hours into such a stretch, was told the
		// recipient was dormant, and reported that Dibs does not deliver
		// messages. It delivers them; it delivers them at the end of the turn,
		// and three separate places said otherwise.
		buys: "mail is USUALLY delivered rather than polled: lifecycle hooks call " +
			"the wake path at turn boundaries, so a question addressed to your agent " +
			"often reaches you without your asking. Not a guarantee, and worth " +
			"knowing which: mid-turn is not a boundary, a plain notify never " +
			"extends a turn, and `wake = none` or a repeated wake suppresses it " +
			"too. check_in each activation is what makes delivery certain; the " +
			"hooks make it convenient. Also installs the dibs skill, so the " +
			"protocol is in context when it is relevant and absent when it is not.",
		install: "claude plugin marketplace add agenxy/dibs && claude plugin install dibs@dibs",
		root:    "~/.claude/plugins/dibs",
		setup: []Step{
			{
				Do: "Install the plugin: `claude plugin marketplace add agenxy/dibs && " +
					"claude plugin install dibs@dibs`. If you cannot run that yourself, ASK " +
					"YOUR OPERATOR to: it is their machine and many harnesses will not let " +
					"an agent change their own configuration. If you have no network or no " +
					"checkout, write the files in `files` below under " +
					"~/.claude/plugins/dibs/ instead: they are the whole plugin.",
				Check: "the files exist at the paths in `files`, with the same contents",
				IfNot: "nothing else here will work; the hooks are what the rest depends on",
			},
			{
				Do: "Restart the session, or start a new one. Hooks are read at session " +
					"start, so a plugin installed mid-session is inert until then.",
				Check: "call spawned_agents and look for YOUR OWN session_id in the result",
				IfNot: "your SessionStart hook did not fire. The plugin is installed but " +
					"not loaded: restarting is the fix, not reinstalling",
				Manual: true,
			},
			{
				Do: "Confirm mail actually arrives rather than merely being fetchable. " +
					"Have another agent send you a message, or send one to yourself from " +
					"a second agent.",
				// The same correction as `buys`, which this escaped: my guard read
				// the summary and not the steps published beside it.
				//
				// hook_poll is bound to SessionStart, UserPromptSubmit, Stop and
				// SubagentStop, and deliverToModel deliberately does not extend a
				// turn for a notify. So mail arrives at a turn boundary, and this
				// told the operator to expect it on the next tool call and to go
				// hunting a PreToolUse hook when it did not appear: a check that
				// fails for a correctly installed plugin, pointed at the wrong file.
				// The engine refuses UserPromptSubmit outright, and refuses Stop
				// under wake=none, a repeated wake, or stop_hook_active. So mail
				// arriving after the preceding Stop can be absent for the whole of
				// the next turn, and a check that says otherwise fails on a
				// correctly installed plugin. Raised by the pre-release review.
				Check: "it reaches you without your calling inbox, at a turn boundary " +
					"rather than the moment it is sent. If it does not, that is not " +
					"proof the plugin is broken: a notify never extends a turn, and " +
					"nothing is delivered mid-turn. check_in is what always shows it",
				IfNot: "the wake hooks are not reaching the daemon. Check that the " +
					"`server` field in hooks.json names the same MCP server you are " +
					"connected through, and that the daemon is the one on this machine",
			},
		},
		verify: "call spawned_agents: if your own session_id is listed, the SessionStart " +
			"hook reached this daemon, which is the only thing that proves the plugin is " +
			"live rather than merely present on disk",
		delivers: true,
	},
	{
		harness: "codex",
		dir:     "codex",
		aliases: []string{"chatgpt-desktop", "chatgpt", "gpt"},
		buys: "mail delivered at a turn boundary, on a build new enough. Codex gained " +
			"a real hooks MCP executor on 2026-08-18 (openai/codex#39296), so an " +
			"`mcp_tool` hook now runs against the session's own MCP runtime: no " +
			"subprocess, and nothing that drives your harness. Dibs ships hooks.json " +
			"for SessionStart, Stop and SubagentStop. Older builds parse the file and " +
			"drop every entry, which is why this said for months that there was no " +
			"wake path here: that was true until it was not, and the note outlived " +
			"the fact. Two limits worth knowing. A hook fires only if the Dibs server " +
			"is ALREADY connected; Codex refuses an unconnected server rather than " +
			"starting one, and connections are established asynchronously, so " +
			"SessionStart can lose that race while Stop is later and likelier. And a " +
			"hook is a callback on YOUR lifecycle: it delivers when your turn ends, " +
			"and nothing outside can make an idle thread wake. For that Codex has its " +
			"own durable queue, which is a harness control surface and not something " +
			"Dibs will reach into.",
		root: "~/.codex",
		setup: []Step{
			{
				Do: "Copy hooks.json into ~/.codex/ (or the config folder for this " +
					"project: hooks are layered, and a project layer overrides the user one).",
				Check: "the file is at ~/.codex/hooks.json and parses: it must have only " +
					"`description` and `hooks` at the top level, because Codex refuses a " +
					"hook file with any key it does not know",
				IfNot: "Codex reports the hook as failed rather than the config as wrong, " +
					"so a typo here looks like Dibs being broken",
			},
			{
				Do: "Check the server name matches. The hook says server \"dibs\", which " +
					"has to be the key under [mcp_servers] in your config.toml.",
				Check: "`mcp_servers.dibs` exists in the config Codex actually loads",
				IfNot: "the hook resolves to no server and fails immediately, because " +
					"Codex will not start a server on a hook's behalf",
			},
			{
				Do: "Keep the pull rhythm anyway: check_in at the start of each " +
					"activation, await_events when you are about to block. On a build " +
					"without the executor this is the whole delivery path, and on a build " +
					"with it, it is what covers the SessionStart race.",
				Check: "await_events returns rather than erroring, and check_in reports a " +
					"cursor serial",
				IfNot: "you are registered but not acknowledging: declare and claim " +
					"refuse until check_in has succeeded this activation",
			},
		},
		verify: "call spawned_agents after a turn ends: if this session is listed, a " +
			"Stop hook reached the daemon and the wake is live. If it is not, the " +
			"build is older than 2026-08-18 or the server was not connected when the " +
			"hook ran, and mail is pull-only until you fix that",
		delivers: false,
	},
}

// For returns the plugin for a harness name as agents actually report it.
//
// Matching is forgiving because the harness string is self-reported free text:
// an agent that says "claude_code", "Claude Code" or "claude" means the same
// thing, and answering "no plugin" to a spelling variant would be the failure
// this package exists to prevent.
func For(harness string) (Plugin, bool) {
	key := strings.ToLower(strings.TrimSpace(harness))
	key = strings.NewReplacer(" ", "-", "_", "-").Replace(key)
	for _, c := range catalog {
		if c.harness == key || containsStr(c.aliases, key) {
			p, err := load(c.harness, c.dir, c.buys, c.install, c.root, c.verify, c.setup, c.delivers)
			if err != nil {
				return Plugin{}, false
			}
			return p, true
		}
	}
	return Plugin{}, false
}

// All returns every plugin, so the resource can list what exists rather than
// only what the caller happens to be running.
func All() []Plugin {
	out := make([]Plugin, 0, len(catalog))
	for _, c := range catalog {
		p, err := load(c.harness, c.dir, c.buys, c.install, c.root, c.verify, c.setup, c.delivers)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Marketplace is the marketplace descriptor, for a host that installs by
// pointing at one rather than by writing files.
func Marketplace() string {
	b, err := files.ReadFile("data/marketplace.json")
	if err != nil {
		return ""
	}
	return string(b)
}

func load(harness, dir, buys, install, root, verify string, setup []Step, delivers bool) (Plugin, error) {
	p := Plugin{
		Harness: harness, Buys: buys, Install: install, Root: root,
		Verify: verify, Setup: setup, Delivers: delivers,
		Files: map[string]string{},
	}
	base := path.Join("data", dir)
	err := fs.WalkDir(files, base, func(p2 string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := files.ReadFile(p2)
		if rerr != nil {
			return rerr
		}
		p.Files[relPath(base, p2)] = string(b)
		return nil
	})
	if err != nil {
		return Plugin{}, err
	}
	return p, nil
}

// relPath works on embed's slash-separated paths, which are not OS paths.
// path/filepath would be wrong on Windows, and these strings go over the wire,
// an agent writing the files out must get the same layout on every platform.
func relPath(base, target string) string {
	return strings.TrimPrefix(strings.TrimPrefix(target, base), "/")
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Names lists the harnesses that have a plugin, sorted, for prose.
func Names() []string {
	out := make([]string, 0, len(catalog))
	for _, c := range catalog {
		out = append(out, c.harness)
	}
	sort.Strings(out)
	return out
}
