// Command version stamps a release across the changelog and every manifest.
//
// The step it replaces was: edit the changelog heading, edit four JSON files,
// remember the date, remember which files, then tag. AGENTS.md says of the
// release pipeline that "no source is updated by hand: if the three ever
// disagree, that is a bug in the pipeline, not a chore", and this was the one
// part still done by hand. It drifted exactly as you would expect, twice.
//
// It stops short of committing and tagging on purpose. A tag is the moment a
// release becomes real: it publishes signed artifacts and writes to the MCP
// registry, and that is the owner's to perform.
//
// It does not move the Homebrew cask, which this said for a while. GoReleaser
// pushes the new cask to a `cask-<version>` branch of the tap and cannot open
// the pull request, because the deploy key can push and cannot call the API, so
// `brew upgrade` serves the previous build until a person merges it.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/agenxy/dibs/internal/release"
)

func main() {
	set := flag.String("set", "", "the version to release, as MAJOR.MINOR.PATCH")
	flag.Parse()
	if *set == "" {
		// A PLACEHOLDER THAT LOOKS LIKE ONE. This printed `0.0.6`, a version
		// already tagged, so anybody copying the usage line got a refusal from
		// the stamper: a release goes forward or not at all.
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/version -set <MAJOR.MINOR.PATCH>   "+
			"(or: task release VERSION=<MAJOR.MINOR.PATCH>)")
		os.Exit(2)
	}
	if err := run(strings.TrimPrefix(*set, "v")); err != nil {
		fmt.Fprintln(os.Stderr, "version:", err)
		os.Exit(1)
	}
}

func run(version string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	changed, err := release.Stamp(root, version)
	if err != nil {
		return err
	}
	for _, f := range changed {
		fmt.Println("  stamped", f)
	}
	fmt.Printf("\n%s is written down. Nothing is committed and nothing is tagged.\n\n"+
		"  Read the diff, then:\n\n"+
		"    git commit -am \"release %s\"\n"+
		"    git tag -a v%s -m \"v%s\" && git push origin main --tags\n\n"+
		"  The tag re-runs the whole gate against that commit before it publishes\n"+
		"  anything, and a manifest that disagrees with it fails there.\n",
		version, version, version, version)
	return nil
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git work tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
