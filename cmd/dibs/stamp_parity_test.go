package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Go and TypeScript stamp rules must agree, forever.
//
// The predicate that decides whether to rewrite somebody's shell command now
// exists twice: in Go for the Claude Code and Codex hooks, and in TypeScript
// for pi's extension, because those harnesses are in different languages and
// neither can call the other. Two copies of a rule this consequential drift,
// one gets a fix, the other does not, and the symptom is a subagent silently
// attributed by a weaker signal on one harness only.
//
// So there is ONE table, here, and it is run through BOTH implementations.
// Adding a case to the Go tests without updating the TypeScript now fails, and
// so does the reverse.
//
// What this covers was measured by injecting drift into the TypeScript, not
// assumed: dropping a harness from the list is caught, and dropping the newline
// guard is caught. Dropping the leading-character or leading-assignment guards
// is NOT, because neither is reachable independently, since a command opening
// with `(`, `>`, `$` or `FOO=` has a first token that is not a bare harness
// name and spawnsAgent has already said no. Those two are defence in depth
// against a future loosening of spawnsAgent, and this test cannot speak for
// them.
func TestTheGoAndTypeScriptStampRulesAgree(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the TypeScript half cannot be run")
	}

	cases := []string{
		// Agent spawns, safe to prefix.
		"codex exec --skip-git-repo-check -m gpt-5.6",
		"/usr/local/bin/codex exec 'do the thing'",
		"claude --print hello",
		"/opt/homebrew/bin/opencode run",
		"pi --model x",
		"  codex exec 'go'  ",
		// Not agents.
		"/Applications/ChatGPT.app/Contents/Resources/codex",
		"go test ./...",
		"grep -r 'codex exec' internal/",
		"echo claude",
		"vim codex.go",
		"",
		"   ",
		// Agent, but not the first command.
		"cd /repo && codex exec 'work'",
		"true; claude --print hi",
		"cat prompt.txt | codex exec",
		// Agent, but a prefix would change the meaning.
		"(codex exec 'go')",
		"{ codex exec; }",
		"> out.txt codex exec",
		"< in.txt codex exec",
		"FOO=1 codex exec",
		"$RUNNER exec",
		"`which codex` exec",
		"'codex' exec",
		"#!/bin/sh",
		"codex exec 'a'\nrm -rf /",
	}

	// The Go answer: both gates, which is what the hook actually applies.
	want := make([]bool, len(cases))
	for i, c := range cases {
		want[i] = spawnsAgent(c) && safeToPrefix(c)
	}

	// The TypeScript answer, from the shipped extension rather than a copy of
	// it: the function is lifted out of the real file, so a change there is
	// what gets tested.
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatalf("reading the pi extension: %v", err)
	}
	const marker = "function stampable(cmd: string): boolean {"
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatalf("plugins/pi/dibs.ts no longer defines stampable(): either it was " +
			"renamed, in which case this test must follow it, or the stamp was removed, " +
			"in which case pi silently stopped attributing its subagents")
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of stampable()")
	}
	fn := string(src)[start : start+end+3]

	table, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	script := fn + "\nconst cases = " + string(table) +
		" as string[]\nconsole.log(JSON.stringify(cases.map(stampable)))\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "parity.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bun, "run", path).Output() // #nosec G204 -- paths this test created
	if err != nil {
		t.Fatalf("running the TypeScript rules: %v\n%s", err, out)
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the TypeScript half did not return a boolean list: %v\n%s", err, out)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d answers for %d cases", len(got), len(want))
	}
	for i, c := range cases {
		if got[i] != want[i] {
			t.Errorf("the two implementations disagree about %q: Go says %v, TypeScript says %v\n"+
				"  Whichever is wrong, one harness is now treating a command differently from\n"+
				"  the others: either rewriting something it should not, or declining to\n"+
				"  attribute a subagent it could have.", c, want[i], got[i])
		}
	}
}
