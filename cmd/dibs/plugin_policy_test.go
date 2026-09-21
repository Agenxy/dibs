package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The TypeScript plugins carry no client policy: they are transports.
//
// This replaces three guards, and the reason it replaces them is the
// finding that ended the pre-release review's longest thread. Rounds
// nineteen through fifty-seven handed `plugins/opencode/dibs.ts` and
// `plugins/pi/dibs.ts`, one at a time, rules the Go bridge already had:
// the host stamp, the repository stamp, canonical paths, the portable
// spelling of a Windows path, the path-argument table, the exception for
// a path named on another agent's behalf, a TLS trust store Node's fetch
// cannot be given, an origin that has to be re-read because the hub
// moves. Each arrived a release after the bridge got it, and each was
// found in behaviour rather than by a test, because a rule that exists
// three times drifts and the symptom is silent on the two copies nobody
// runs. The old guards pinned the copies to each other, which kept them
// in step and left three of them.
//
// So the copies are gone: both plugins now speak JSON-RPC to
// `dibs mcp-stdio` and that binary decides everything about what a call
// carries. This test is what stops them growing back. It is deliberately
// crude, a list of things a transport has no business containing, and a
// new entry costs one line: the alternative is finding out a release
// later, on a harness nobody here runs.
//
// The positive half matters as much. A file that contains none of these
// because it no longer talks to Dibs at all would pass the list and fail
// the product, so each plugin must also be seen to spawn the bridge.
func TestThePluginsHoldNoClientPolicy(t *testing.T) {
	// Each entry: a pattern a transport must not contain, and what the
	// reader should do about it.
	forbidden := []struct {
		name string
		re   *regexp.Regexp
		why  string
	}{
		{
			"the local secret",
			regexp.MustCompile(`X-Dibs-Local|local\.secret`),
			"authenticating to the daemon is the bridge's job; a plugin that reads the " +
				"secret is a second client with its own idea of which daemon to hand it to",
		},
		{
			"an HTTP endpoint",
			regexp.MustCompile(`"/api/|/api/[a-z-]+`),
			"the bridge speaks to the daemon; a plugin that posts to /api/ bypasses " +
				"every rule the bridge applies on the way",
		},
		{
			"a TLS trust store",
			regexp.MustCompile(`node:https|node:tls|\bca:\s|rejectUnauthorized`),
			"the trust `dibs trust` recorded is the binary's; Node's fetch cannot be " +
				"given it, which is the defect round fifty-one found in pi",
		},
		{
			"the portable spelling of a path",
			regexp.MustCompile(`replace\(/\\\\|realpath|win32`),
			"one implementation of the separator rule, in Go, applied to whatever " +
				"arrives; a copy here is the bug rounds forty-two to forty-seven kept finding",
		},
		{
			"a table of which arguments are paths",
			regexp.MustCompile(`PATH_ARGS|pathArgs`),
			"the bridge knows which arguments name paths, and it knows for the tools " +
				"added after this file was last touched",
		},
		{
			"this machine's identity",
			regexp.MustCompile(`DIBS_HOST_ID|host_id|hostID`),
			"a machine's identity is a fact its data directory records and the daemon " +
				"arbitrates; a plugin that mints one splits the board",
		},
		{
			"the repository stamp",
			regexp.MustCompile(`rev-parse|git remote|"remote"`),
			"the bridge reads the checkout it runs in; two readers disagree the first " +
				"time one of them is run from a subdirectory",
		},
		{
			"where the daemon lives",
			regexp.MustCompile(`DIBS_ADDR|DIBS_DIR|127\.0\.0\.1`),
			"the address moves when a board joins a hub, and the binary re-reads it; a " +
				"plugin that captured it at load time talks to yesterday's daemon",
		},
	}

	for _, plugin := range []string{"opencode", "pi"} {
		t.Run(plugin, func(t *testing.T) {
			path := filepath.Join("..", "..", "plugins", plugin, "dibs.ts")
			raw, err := os.ReadFile(path) // #nosec G304 -- a file in this repository
			if err != nil {
				t.Fatalf("reading the %s plugin: %v", plugin, err)
			}
			code := withoutComments(string(raw))

			for _, f := range forbidden {
				if loc := f.re.FindString(code); loc != "" {
					t.Errorf("plugins/%s/dibs.ts contains %s (%q). %s.\n"+
						"If the bridge is genuinely missing something this plugin needs, "+
						"add it to the bridge and call it; do not re-derive it here.",
						plugin, f.name, loc, f.why)
				}
			}

			if !strings.Contains(code, `"mcp-stdio"`) {
				t.Errorf("plugins/%s/dibs.ts never spawns `dibs mcp-stdio`: it holds no "+
					"client policy because it is no longer a client at all", plugin)
			}
		})
	}
}

// Strips TypeScript comments so the prose explaining a rule is not read
// as the rule. Line comments and `*`-continued block lines only, which
// is every comment in these two files and cannot mistake a `//` inside a
// string literal for the start of one.
func withoutComments(src string) string {
	var kept []string
	block := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case block:
			if strings.Contains(t, "*/") {
				block = false
			}
		case strings.HasPrefix(t, "/*"):
			if !strings.Contains(t, "*/") {
				block = true
			}
		case strings.HasPrefix(t, "//"), strings.HasPrefix(t, "*"):
			// a comment line, or a continuation of one
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
