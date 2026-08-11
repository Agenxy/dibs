package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every verb the dispatch accepts must be completed in every shell.
//
// The completion scripts are generated from the verb table precisely so the
// two cannot drift, and this is the test that makes the drift a build failure
// rather than a verb that quietly stops completing. The verbs are read out of
// main.go's case labels, the same source TestEverySubcommandIsInTheUsageText
// reads, so the check holds against the dispatch itself and not against a
// second list that could go stale with it.
func TestEveryDispatchedVerbIsCompletedInEveryShell(t *testing.T) {
	verbs := dispatchedVerbs(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		for _, verb := range verbs {
			// As a whole word, not a substring: "log" inside some other token
			// would satisfy strings.Contains while completing nothing.
			word := regexp.MustCompile(`(^|[^a-z-])` + regexp.QuoteMeta(verb) + `($|[^a-z-])`)
			if !word.MatchString(script) {
				t.Errorf("`agents %s` is dispatched but missing from the %s completion:\n"+
					"  a verb that does not complete does not exist to anyone who relies on\n"+
					"  the completion to remember the verbs", verb, shell)
			}
		}
	}
}

// Each script has to survive its own shell's parser, or sourcing it breaks the
// user's shell startup: which is where these scripts live.
//
// A syntax check, not an execution: completion behaviour needs an interactive
// completion context no test has, but a script the parser rejects can never
// reach one. Shells the machine does not have are skipped rather than failed,
// because their absence says nothing about the script.
func TestGeneratedCompletionsParseInTheirShells(t *testing.T) {
	for shell, ext := range map[string]string{"bash": "bash", "zsh": "zsh", "fish": "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed here", shell)
			}
			script, err := completionScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "agents."+ext)
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, "-n", path).CombinedOutput()
			if err != nil {
				t.Fatalf("%s rejected the generated script: %v\n%s", shell, err, out)
			}
		})
	}
}

// The refusal must name the shells that work: the reader's next move is
// retyping the command, and making them guess the spelling turns one failed
// attempt into three.
func TestCompletionNamesTheShellsWhenRefusing(t *testing.T) {
	for _, typed := range []string{"", "powershell"} {
		_, err := completionScript(typed)
		if err == nil {
			t.Fatalf("completionScript(%q) must refuse", typed)
		}
		for _, want := range []string{"bash", "zsh", "fish"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %q does not name %s: %v", typed, want, err)
			}
		}
	}
}
