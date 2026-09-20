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
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// WHICH codex, said out loud. Two live on this machine: a mise shim at
	// 0.144.3 first on PATH and the 0.153.4 inside ChatGPT.app, and the older
	// one cannot run the configured model. The failure read as a model error
	// and cost a round trip before anyone asked which binary had been found.
	// DIBS_REVIEWER names one explicitly; otherwise PATH decides, and either
	// way the choice and its version are printed before a token is spent.
	reviewer := os.Getenv("DIBS_REVIEWER")
	var err error
	if reviewer == "" {
		reviewer, err = exec.LookPath("codex")
	}
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

	// #nosec G204 G702 -- the reviewer is named by the operator, through PATH or
	// DIBS_REVIEWER in their own shell, which is the same trust as any program
	// they run by hand. Nothing an agent or a message says reaches it.
	if v, verr := exec.Command(reviewer, "--version").Output(); verr == nil {
		fmt.Fprintf(os.Stderr, "reviewer: %s (%s)\n", reviewer, strings.TrimSpace(string(v)))
	} else {
		fmt.Fprintf(os.Stderr, "reviewer: %s (version unknown: %v)\n", reviewer, verr)
	}
	cmd := exec.Command(reviewer, "exec", prompt) // #nosec G204 G702 -- see above: the operator's own reviewer
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
	// The reviewer's output goes to the terminal AND through a check for the
	// one line the brief asks it to end with. Twice now a run has exited 0
	// while reviewing nothing: once with no findings and no explanation, and
	// once saying in so many words that it could not read the diff (its tool
	// router was broken) while the task still reported success. A gate that
	// passes when the reviewer says "I did not look" is not a gate.
	var seen strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &seen)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	count, ok := findingsLine(seen.String())
	if !ok {
		return errors.New("the reviewer did not end with a FINDINGS: <count> line, so it did " +
			"not review the surface (or did not say so); read its output above, fix what " +
			"stopped it, and run again")
	}
	fmt.Fprintf(os.Stderr, "reviewer reported %d finding(s)\n", count)
	return nil
}

// findingsLine finds the brief's closing line. It is the last `FINDINGS: <n>`
// in the output, so a reviewer that quotes the brief before answering is not
// mistaken for one that answered.
func findingsLine(out string) (int, bool) {
	count, ok := -1, false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, "FINDINGS:")
		if !found {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			continue
		}
		count, ok = n, true
	}
	return count, ok
}

func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).Output() // #nosec G204 -- fixed argv
	return strings.TrimSpace(string(b)), err
}
