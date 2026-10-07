// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// casknote writes the release job's "the cask still needs a person" summary.
//
// A Go program rather than a `run: |` block because this repository does not
// use shell for build or release steps, and a workflow's embedded script is a
// shell script that happens to live in YAML: same untyped string handling, same
// silent continuation past failure, and it cannot be built, vetted or tested.
// The pre-release review found this one because a `${VAR#prefix}` expansion and
// a brace group had appeared here; the hygiene guard could not, because it
// looked for file extensions and shebangs.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "casknote:", err)
		os.Exit(1)
	}
}

func run() error {
	// The tag, without its leading v: the cask branch is named for the version.
	version := strings.TrimPrefix(os.Getenv("GITHUB_REF_NAME"), "v")
	if version == "" {
		return fmt.Errorf("GITHUB_REF_NAME is empty, so there is no version to " +
			"name a cask branch after. This runs on a tag-ref publication dispatch")
	}
	summary := os.Getenv("GITHUB_STEP_SUMMARY")
	if summary == "" {
		return fmt.Errorf("GITHUB_STEP_SUMMARY is not set, so this note would go " +
			"nowhere. That file is the whole point: a green release job otherwise " +
			"implies the cask moved, and it has not")
	}
	// GITHUB_STEP_SUMMARY is a path the runner sets for exactly this and has
	// already created; the mode applies only if it somehow does not exist.
	// #nosec G304,G302,G703 -- see above
	f, err := os.OpenFile(filepath.Clean(summary), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, `### Homebrew cask

Verified the signed release cask on tap main or `+"`cask-%s`"+` in agenxy/homebrew-tap.
**If the review branch is not merged, `+"`brew upgrade`"+` still serves the previous build.**

https://github.com/Agenxy/homebrew-tap/compare/cask-%s?expand=1
`, version, version)
	return err
}
