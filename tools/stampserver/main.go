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
		// VERIFIED AGAINST A REAL RELEASE, because this branch took any string.
		//
		// The comment above says this order "cannot invent one nobody shipped",
		// and the explicit branch could: a manual dispatch with `-version 9.9.9`
		// stamped and published 9.9.9 to the registry, for a version that was
		// never built, tagged or released. That is precisely the hole the
		// release job's `needs:` was added to close, reachable through the
		// recovery path beside it, and the changelog claims it is shut. Found by
		// the pre-release review, which reproduced it read-only.
		//
		// A dispatch with no version still means "whatever is currently
		// released", which is the branch below and needs no such check.
		want := strings.TrimPrefix(v, "v")
		// A VERSION, CHECKED AS ONE, BEFORE IT BECOMES AN ARGUMENT.
		//
		// This went straight to `gh release view <want>` positionally, and a
		// value beginning with a dash is not positional at all: `--help` was
		// parsed by gh as an OPTION, exited zero, and the release-existence check
		// read that as "the release is there". The manifest was then stamped,
		// printing "publishing version --help". The check I added last round was
		// real and its input was not. Found by the pre-release review.
		if !semver.MatchString(want) {
			return "", fmt.Errorf("%q is not a version. Give a semantic version like "+
				"1.2.3, or no -version at all to publish whatever is currently "+
				"released", v)
		}
		if err := mustBeReleased(want); err != nil {
			return "", err
		}
		return want, nil
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

// mustBeReleased refuses a version GitHub has no release for.
//
// The registry is public and permanent: a version published there that nobody
// can download is worse than a failed job, because every client that indexes
// from it now offers an install that cannot succeed.
//
// Skipped only when there is nothing to ask, which is a local run: refusing
// there would make the tool untestable without a network, and a local run
// publishes nothing.
func mustBeReleased(version string) error {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil
	}
	if !repoName.MatchString(repo) {
		return fmt.Errorf("GITHUB_REPOSITORY is %q, which is not owner/name", repo)
	}
	for _, tag := range []string{"v" + version, version} {
		args := []string{"release", "view", tag, "--repo", repo, "--json", "tagName"}
		// #nosec G204,G702 -- argv, not a shell line; repo is shape-checked above
		// and tag is the version we are being asked to publish.
		if err := exec.Command("gh", args...).Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s has no release for %s, so publishing it to the registry "+
		"would advertise an install nobody can complete. Tag and release it "+
		"first, or run this with no -version to publish whatever is currently "+
		"released", repo, version)
}

// semver is the shape a published version has: digits and dots, with the
// optional prerelease and build parts the registry accepts. Anchored, and
// deliberately refusing anything that could be read as a flag.
var semver = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// repoName is the owner/name shape GITHUB_REPOSITORY always has.
var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
