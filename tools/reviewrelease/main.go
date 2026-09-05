// Command reviewrelease runs the pre-release review: a model that is NOT the
// one that wrote the change reads everything since the last tag.
//
// A Go program rather than a `cmd:` block, because the no-shell rule is about
// what shell IS and not where the bytes live. A multiline task command with
// conditionals, substitution and redirection is a shell script that happens to
// be indented under YAML: it cannot be built, vetted or run on its own, and the
// hygiene guard that reads workflow `run:` blocks never looked at Taskfile
// `cmd:` blocks, so this one sat in the tree while the changelog claimed the
// class was removed and guarded. Found by the pre-release review, about the
// task that runs the pre-release review.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	reviewer, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("no reviewer found: install codex, or run docs/REVIEW.md's " +
			"brief through whichever model you have; the point is that it is not the " +
			"one that wrote the change")
	}
	brief, err := os.ReadFile("docs/REVIEW.md")
	if err != nil {
		return fmt.Errorf("reading the brief: %w", err)
	}
	tag, err := output("git", "describe", "--tags", "--abbrev=0")
	if err != nil {
		return fmt.Errorf("finding the last tag: %w", err)
	}
	prompt := string(brief) + "\n\nThe diff under review is: git diff " + tag +
		"..HEAD\nRead it with git, in pieces if you need to. Work read-only."

	cmd := exec.Command(reviewer, "exec", prompt) // #nosec G204 -- resolved by LookPath
	// STDIN CLOSED, and this is the reason the task existed as a script.
	//
	// `codex exec` waits on stdin when it is not a terminal, so run from a
	// script, or after anything that consumed stdin, it printed "Reading
	// additional input from stdin..." and sat there until killed. A release gate
	// that hangs instead of reviewing is a release gate somebody stops running.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()
	cmd.Stdin = devNull
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).Output() // #nosec G204 -- fixed argv
	return strings.TrimSpace(string(b)), err
}
