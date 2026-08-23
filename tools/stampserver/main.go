// stampserver writes the released version into server.json.
//
// Replaces a `run: |` block of shell conditionals. This repository does not use
// shell for build or release steps, and that block had the shape those go wrong
// in: three branches choosing a version, `${{ }}` template values interpolated
// directly into shell words, and a `jq` filter piped through a temporary file
// with the move outside any error check.
//
// THE TAG IS THE TRUTH. server.json carries a version so the file is valid on
// its own and readable in the tree, but what gets published is whatever was
// actually released.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func main() {
	path := flag.String("file", "server.json", "the manifest to stamp")
	explicit := flag.String("version", "", "the version to write, if the caller knows it")
	flag.Parse()
	// The workflow passes it in the ENVIRONMENT rather than on the command
	// line: `${{ }}` is substituted as text before the command runs, so a
	// dispatch input written into the run line becomes part of the command.
	if *explicit == "" {
		*explicit = os.Getenv("DIBS_PUBLISH_VERSION")
	}
	if err := run(*path, *explicit); err != nil {
		fmt.Fprintln(os.Stderr, "stampserver:", err)
		os.Exit(1)
	}
}

func run(path, explicit string) error {
	version, err := resolve(explicit)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a path from our own workflow
	if err != nil {
		return err
	}
	// Decoded into a map rather than a struct: this file has fields this
	// program has no business knowing about, and rewriting it through a struct
	// would silently drop every one of them.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	doc["version"] = version
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil { // #nosec G306 -- a published manifest
		return err
	}
	// Said out loud, because the job that follows publishes it. The shell this
	// replaced echoed the version and cat'd the file for the same reason, and a
	// silent stamp is one more step whose log does not say what it did.
	fmt.Printf("publishing version %s from %s\n", version, path)
	return nil
}

// resolve answers which version is being published, in the order that cannot
// invent one nobody shipped.
func resolve(explicit string) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return strings.TrimPrefix(v, "v"), nil
	}
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		if v := os.Getenv("GITHUB_REF_NAME"); v != "" {
			return strings.TrimPrefix(v, "v"), nil
		}
	}
	// A dispatch with no version publishes whatever is currently released,
	// which is the only answer that cannot invent a version nobody shipped.
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return "", errors.New("no version given, not running on a tag, and " +
			"GITHUB_REPOSITORY is unset, so there is nothing to ask")
	}
	// CHECKED BEFORE IT IS USED. This is argv rather than a shell line, so
	// nothing here can be word-split into a second command, but `gh` takes
	// flags and an owner/name that started with a dash would become one. The
	// shape is fixed and cheap to insist on.
	if !repoName.MatchString(repo) {
		return "", fmt.Errorf("GITHUB_REPOSITORY is %q, which is not owner/name", repo)
	}
	args := []string{"release", "view", "--repo", repo, "--json", "tagName", "--jq", ".tagName"}
	// #nosec G204,G702 -- argv, not a shell line, and repo is matched against
	// repoName immediately above so it cannot begin with a dash
	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		return "", fmt.Errorf("asking for the current release of %s: %w", repo, err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("%s has no published release to take a version from", repo)
	}
	return strings.TrimPrefix(v, "v"), nil
}

// repoName is the owner/name shape GITHUB_REPOSITORY always has.
var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
