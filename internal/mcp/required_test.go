package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every tool declares `required` in its schema, and for a long time nothing
// enforced it. Arguments unmarshal into one struct, so an omitted parameter
// became that field's zero value and the handler answered about the zero value
// as though the caller had supplied it.
//
// Found by making the mistake: acknowledging an announcement with `serial`
// instead of `msg_serial` returned "no announcement at serial 0" — a serial
// that appears nowhere in the request. The obvious reading is that the
// announcement is gone, and the actual fault, a parameter name, is not
// mentioned at all. A confident and specific error for a cause that is fiction.
func TestAMisnamedParameterIsNamed(t *testing.T) {
	for _, c := range []struct {
		tool, args string
		want       []string
	}{
		{
			"lane_ack", `{"token":"t","serial":14}`,
			[]string{"msg_serial", "serial", "does not take"},
		},
		{
			"respond", `{"token":"t","serial":22,"disposition":"answer"}`,
			[]string{"msg_serial", "serial"},
		},
		// A plain omission names what is missing and does not invent a culprit.
		{"claim", `{"token":"t"}`, []string{"path", "mode"}},
		// An explicit null is not an answer.
		{"lane_ack", `{"token":"t","msg_serial":null}`, []string{"msg_serial"}},
	} {
		err := checkRequired(c.tool, json.RawMessage(c.args), "")
		if err == nil {
			t.Errorf("%s(%s): accepted a call missing a required parameter", c.tool, c.args)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s(%s): error %q does not mention %q", c.tool, c.args, err, want)
			}
		}
	}
}

// Enforcement must not break the calls that are correct — including the two
// legitimate ways a token arrives.
func TestValidCallsAreNotRejected(t *testing.T) {
	for _, c := range []struct {
		tool, args, bearer string
	}{
		{"ack_board", `{"token":"t"}`, ""},
		// The token may come from the Authorization header instead of the
		// arguments object, and the arguments object alone cannot see it.
		{"ack_board", `{}`, "t"},
		{"claim", `{"token":"t","path":"/x","mode":"exclusive"}`, ""},
		{"lane_ack", `{"token":"t","msg_serial":14}`, ""},
		// hook_poll requires session_id, but an EMPTY one is legitimate: the
		// documented fallback is that a harness whose session id differs from
		// the registered one is found by cwd instead. Present-but-empty is an
		// answer; absent is not. Enforcement must not collapse the two, because
		// that would break every harness relying on the fallback.
		{"hook_poll", `{"session_id":"","cwd":"/work","event":"Stop"}`, ""},
		{"hook_poll", `{"session_id":"s1","event":"Stop"}`, ""},
	} {
		if err := checkRequired(c.tool, json.RawMessage(c.args), c.bearer); err != nil {
			t.Errorf("%s(%s) bearer=%q: rejected a valid call: %v", c.tool, c.args, c.bearer, err)
		}
	}
}

// The index is derived from toolDefs rather than restated, so a tool cannot
// declare one contract in its schema and be held to another. This checks the
// derivation actually found the schemas — an empty map would make every check
// above vacuously pass.
func TestRequiredIsDerivedFromTheSchemasThemselves(t *testing.T) {
	if len(requiredParams) < 10 {
		t.Fatalf("only %d tools have required params indexed — the derivation is broken, "+
			"and every enforcement test above would pass vacuously", len(requiredParams))
	}
	for tool, req := range requiredParams {
		known := map[string]bool{}
		for _, p := range knownParams[tool] {
			known[p] = true
		}
		for _, r := range req {
			if !known[r] {
				t.Errorf("%s requires %q but does not declare it as a property — "+
					"a parameter no caller can satisfy", tool, r)
			}
		}
	}
}

// The shipped hook definitions must satisfy the schemas they call.
//
// Nothing connected the two before: hooks.json is data, the schemas are Go, and
// a rename on either side would be found by an operator whose edits stopped
// being guarded — a silent failure, since guard_path fails open by design.
//
// Enforcement of `required` made this worse before it made it better: with the
// schemas now actually binding, a hook that omits a required parameter goes
// from "quietly wrong" to "hard error at every edit". So the two artifacts get
// checked against each other here.
func TestShippedHooksSatisfyTheSchemasTheyCall(t *testing.T) {
	// EVERY shipped plugin, not one hardcoded path. Codex loads hooks from the
	// same `hooks/hooks.json` layout as Claude Code — deliberately, its own
	// feature flag calls them "Claude-style" — so Lanes will ship more than one
	// of these, and a second plugin referencing a tool that does not exist would
	// have sailed past a test that only ever opened the first.
	files, _ := filepath.Glob("../../plugins/*/hooks/hooks.json")
	if len(files) == 0 {
		t.Skip("no shipped hook definitions to check")
	}

	seen := 0
	where := ""
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if v["type"] == "mcp_tool" {
				tool, _ := v["tool"].(string)
				input, _ := v["input"].(map[string]any)
				args, _ := json.Marshal(input)
				seen++
				// The bearer token is supplied by the host's MCP config, not by
				// the hook, so hooks legitimately omit it.
				if err := checkRequired(tool, args, "hook-supplied-token"); err != nil {
					t.Errorf("%s calls %s but the schema rejects it: %v", where, tool, err)
				}
				if _, known := knownParams[tool]; !known {
					t.Errorf("%s calls %q, which is not a tool this server serves —\n"+
						"  a hook wired to a tool that does not exist fails silently at runtime,\n"+
						"  which is indistinguishable from the hook never having been written",
						where, tool)
				}
			}
			for _, c := range v {
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f) // #nosec G304 -- a repo path from a glob
		if err != nil {
			t.Errorf("cannot read %s: %v", f, err)
			continue
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s is not valid JSON: %v", f, err)
			continue
		}
		where = f
		walk(doc)
	}

	if seen == 0 {
		t.Error("found no mcp_tool hooks to check — the walk is broken, and this " +
			"test would pass no matter how wrong hooks.json got")
	}
}

// A tool the server HANDLES but does not DECLARE is a tool no agent can use.
//
// vouch_child was in exactly that state, and it mattered more than a missing
// entry usually would. register_lane's `parent` parameter told agents "you
// inherit its lanes and do not join, queue or count separately" — but lineage
// grants nothing unless the parent vouched with a one-time nonce, and
// vouch_child, the only way to issue one, was absent from tools/list. So the
// documented inheritance was unreachable: every subagent naming a parent got
// none of what the parameter promised, silently, and was queued like a stranger.
//
// Nothing failed. That is the point — a capability that quietly does nothing
// reads as a capability that works.
func TestEveryDispatchedToolIsDeclared(t *testing.T) {
	src, err := os.ReadFile("mcp.go")
	if err != nil {
		t.Fatalf("reading mcp.go: %v", err)
	}
	declared := map[string]bool{}
	for _, d := range toolDefs {
		if n, _ := d["name"].(string); n != "" {
			declared[n] = true
		}
	}
	// MCP protocol methods and Go type-switch cases share the `case "x":` shape;
	// only names that map to a core op are tools.
	protocol := map[string]bool{
		"initialize": true, "ping": true, "string": true, "bool": true,
		"number": true, "float64": true, "int": true,
	}

	re := regexp.MustCompile(`case "([a-z_]+)":`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		name := m[1]
		if protocol[name] || declared[name] {
			continue
		}
		t.Errorf("%q is dispatched but not in toolDefs — it is absent from tools/list, "+
			"so no agent can discover or call it, and nothing reports that", name)
	}

	if len(declared) < 30 {
		t.Errorf("only %d tools declared — the parse is broken and this test would "+
			"pass vacuously", len(declared))
	}
}

// A call that quietly does nothing is worse than a call that fails.
//
// The unknown-argument check used to live behind "and something required is
// missing", so a WELL-FORMED call carrying a misnamed field was answered
// {"ok": true} and changed nothing. update_lane accepts only "description";
// update_lane(pid: 1234) reported success and did not touch the lane. Found by
// driving a real board and wondering why three lanes stayed stale after I had
// just pointed them at live processes.
//
// This is the worst failure this server can produce. An agent cannot see the
// board it is not looking at — its only evidence that anything happened is
// what these tools return. Answering "ok" for work not done means the agent
// proceeds on it, and the entire product is other agents trusting that.
func TestAWellFormedCallWithAMisnamedFieldIsRefused(t *testing.T) {
	for _, c := range []struct{ tool, args, want string }{
		// The one that was actually found, and the shape of it: every required
		// field present, so nothing was "missing", and a typo rides along free.
		{"update_lane", `{"token":"t","pid":1234}`, "pid"},
		{"update_lane", `{"token":"t","desc":"x"}`, "desc"},
		{"set_slot", `{"token":"t","text":"x","urgency":"high"}`, "urgency"},
	} {
		err := checkRequired(c.tool, []byte(c.args), "")
		if err == nil {
			t.Errorf("%s(%s) was accepted — a field the tool does not take was "+
				"ignored and the caller told it succeeded", c.tool, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the refusal must name the offending field %q, got: %v",
				c.tool, c.want, err)
		}
		// It must also say nothing happened. "Invalid argument" leaves an agent
		// unsure whether it half-applied.
		if !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("%s: the refusal must say nothing was changed, got: %v", c.tool, err)
		}
	}
}

// And a correct call must still be a correct call — strictness that rejects
// valid work is a worse bug than the one it fixes.
func TestValidCallsAreStillAccepted(t *testing.T) {
	for _, c := range []struct{ tool, args string }{
		{"update_lane", `{"token":"t","description":"new"}`},
		{"set_slot", `{"token":"t","text":"doing the thing"}`},
		// Session-addressed hook tools do not declare "token" and our own
		// shipped hooks send it: authentication is orthogonal to the domain
		// schema and must not be read as a misnamed field.
		{"hook_poll", `{"token":"t","session_id":"s","event":"Stop"}`},
		{"guard_path", `{"token":"t","session_id":"s","path":"/tmp/x"}`},
	} {
		if err := checkRequired(c.tool, []byte(c.args), ""); err != nil {
			t.Errorf("%s(%s) was refused: %v", c.tool, c.args, err)
		}
	}
}

// Everything the bridge injects must be a parameter the tool declares.
//
// `lanes mcp-stdio` enriches register_lane with what it can discover about the
// caller — cwd, branch, host, harness, surface, session id, title. Three of
// those (surface, harness, host) were never in the schema. The server stored
// them anyway, so nothing looked broken; but no agent could discover them, no
// documentation described them, and the moment unknown arguments started being
// REFUSED rather than ignored, the bridge's own calls were rejected. Every
// real harness path — opencode, codex, pi — went through that call.
//
// A schema that under-describes its tool is not a smaller problem than one
// that over-describes it. This pins the two together.
func TestTheBridgeOnlySendsFieldsTheSchemaDeclares(t *testing.T) {
	// Kept as a literal list rather than parsed out of cmd/lanes: this must
	// fail when somebody ADDS an injection there, and a parser that follows
	// the source automatically would simply agree with it.
	injected := []string{
		"pid", "cwd", "branch", "host", "harness", "surface", "session_id", "title",
	}
	known := map[string]bool{}
	for _, p := range knownParams["register_lane"] {
		known[p] = true
	}
	for _, f := range injected {
		if !known[f] {
			t.Errorf("the stdio bridge injects %q into register_lane and the schema does not declare it\n"+
				"  the value is stored but undiscoverable, and strict argument checking will refuse\n"+
				"  every call the bridge makes — which is every real harness", f)
		}
	}
	// And the check has to be looking at something. A typo in the tool name
	// would make this pass against an empty set.
	if len(known) < len(injected) {
		t.Fatalf("register_lane declares %d params, fewer than the %d the bridge injects — "+
			"this test is not reading the schema it thinks it is", len(known), len(injected))
	}
}

// A PreToolUse `command` hook must not be able to fail.
//
// Claude Code treats a non-zero PreToolUse hook as a REJECTION: the tool call
// is blocked. So a hook that shells out has the power to break every Bash
// command the agent issues, and it exercised that power — a `lanes hook-spawn`
// hook shipped against an older `lanes` binary that had no such subcommand,
// which exited 2 AND printed its usage text to stdout, where hook output is
// parsed. Every Bash invocation in that session was rejected. Observed in a
// real session, not reasoned about.
//
// Version skew between a plugin and the binary it calls is the normal state of
// an install that has been upgraded halfway, so the hook has to survive it: run
// the command, emit its output only if it succeeded AND looks like the JSON a
// hook returns, and exit 0 no matter what.
func TestCommandHooksCannotBreakTheToolTheyDecorate(t *testing.T) {
	files, _ := filepath.Glob("../../plugins/*/hooks/hooks.json")
	if len(files) == 0 {
		t.Skip("no shipped hook definitions")
	}
	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f) // #nosec G304 -- a repo path from a glob
		if err != nil {
			t.Errorf("cannot read %s: %v", f, err)
			continue
		}
		var doc struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for event, entries := range doc.Hooks {
			for _, e := range entries {
				for _, h := range e.Hooks {
					if h.Type != "command" {
						continue
					}
					checked++
					if !strings.Contains(h.Command, "exit 0") {
						t.Errorf("%s %s runs a command hook that can fail:\n  %s\n"+
							"  A non-zero PreToolUse hook BLOCKS the tool call. This must end in\n"+
							"  `exit 0` so a missing or outdated binary degrades to no stamping\n"+
							"  rather than to an agent that cannot run anything.",
							f, event, h.Command)
					}
					if !strings.Contains(h.Command, `case "$out" in`) {
						t.Errorf("%s %s emits its command's stdout unconditionally:\n  %s\n"+
							"  Hook stdout is PARSED. An older binary printed its usage text there\n"+
							"  and the harness tried to read it as a hook decision.", f, event, h.Command)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no command hooks were examined — the walk is broken and this test " +
			"would pass however dangerous the hooks became")
	}
}
