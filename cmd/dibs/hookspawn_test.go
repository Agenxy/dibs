package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The stamp goes on commands that spawn an agent, and nothing else.
//
// This runs in front of EVERY shell command an agent issues, so a false
// positive rewrites something it had no business touching. It shares
// liveness.HarnessOf with the process sweep deliberately: a command stamped
// here but unrecognised there would be attributed and never watched, and the
// reverse would be watched and never attributed.
func TestOnlyAgentSpawnsAreStamped(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want bool
	}{
		{"codex exec --skip-git-repo-check -m gpt-5.6", true},
		{"/usr/local/bin/codex exec 'do the thing'", true},
		{"claude --print hello", true},
		{"opencode run", true},

		// Not agents.
		{"go test ./...", false},
		{"grep -r 'codex exec' internal/", false},
		{"echo claude", false},
		{"", false},

		// The agent is not the FIRST command, so a leading assignment would
		// bind to `cd` and never reach it. A stamp that silently does nothing
		// is worse than none: the fallback rungs would have caught this.
		{"cd /repo && codex exec 'work'", false},
		{"true; claude --print hi", false},
		{"cat prompt.txt | codex exec", false},
	} {
		if got := spawnsAgent(c.cmd); got != c.want {
			t.Errorf("spawnsAgent(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// A hook that mangles a command is far worse than one that does nothing: the
// agent's work fails in a way it cannot diagnose, on a machine where nobody
// knew a hook was involved. So anything a leading assignment would change the
// meaning of is refused.
func TestAnythingAPrefixWouldChangeIsRefused(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want bool
	}{
		{"codex exec 'go'", true},
		{"  codex exec 'go'  ", true},

		{"(codex exec 'go')", false},        // subshell
		{"{ codex exec; }", false},          // group
		{"> out.txt codex exec", false},     // leading redirect
		{"< in.txt codex exec", false},      // leading redirect
		{"FOO=1 codex exec", false},         // an assignment already leads
		{"$RUNNER exec", false},             // expansion decides the program
		{"`which codex` exec", false},       // ditto
		{"'codex' exec", false},             // quoted head
		{"#!/bin/sh", false},                // not a command
		{"codex exec 'a'\nrm -rf /", false}, // multi-line: binds to line one only
		{"", false},
	} {
		if got := safeToPrefix(c.cmd); got != c.want {
			t.Errorf("safeToPrefix(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// The two gates compose: a command must both spawn an agent AND be safe to
// prefix. Neither alone is sufficient, and the ones that pass one but not the
// other are exactly where a careless implementation does damage.
func TestBothGatesMustAgreeBeforeAnythingIsRewritten(t *testing.T) {
	// Spawns an agent, but rewriting it would be wrong.
	for _, cmd := range []string{"(codex exec 'go')", "FOO=1 codex exec", "> log codex exec"} {
		if spawnsAgent(cmd) && safeToPrefix(cmd) {
			t.Errorf("%q passed both gates; it spawns an agent but must not be rewritten", cmd)
		}
	}
	// Safe to prefix, but there is no agent to attribute.
	for _, cmd := range []string{"go test ./...", "ls -la"} {
		if spawnsAgent(cmd) {
			t.Errorf("%q was treated as an agent spawn", cmd)
		}
	}
}

// The whole decision, without a daemon or a pipe.
func TestStampIsEmittedOnlyForTheOneCaseThatDeserevesIt(t *testing.T) {
	agent := func(string, string) string { return "builder" }
	none := func(string, string) string { return "" }

	hook := func(cmd string) string {
		return `{"session_id":"s","cwd":"/repo","tool_name":"Bash","tool_input":{"command":` +
			mustJSON(cmd) + `}}`
	}

	got := stampFor(strings.NewReader(hook("codex exec 'review'")), agent)
	if !strings.Contains(got, `DIBS_PARENT=builder codex exec`) {
		t.Errorf("a real agent spawn from a known agent was not stamped: %q", got)
	}
	// The rewrite must preserve the rest of the command exactly; a stamp that
	// quietly drops an argument is the mangling this is built to avoid.
	if !strings.Contains(got, `review`) {
		t.Errorf("the original command did not survive the rewrite: %q", got)
	}

	for name, in := range map[string]string{
		"not an agent":     hook("go test ./..."),
		"unsafe to prefix": hook("(codex exec 'go')"),
		"already stamped":  hook("DIBS_PARENT=other codex exec 'go'"),
		"not a shell tool": `{"session_id":"s","tool_input":{"file_path":"/x"}}`,
		"malformed input":  `not json at all`,
	} {
		if out := stampFor(strings.NewReader(in), agent); out != "" {
			t.Errorf("%s produced a rewrite: %q", name, out)
		}
	}
	// A session with no agent is the common case on a machine running Dibs for
	// one project among many, and must cost the command nothing.
	if out := stampFor(strings.NewReader(hook("codex exec 'go'")), none); out != "" {
		t.Errorf("a session with no agent produced a rewrite: %q", out)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
