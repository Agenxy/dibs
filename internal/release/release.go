// Package release is the single declaration of what carries the release
// version, and the only thing that writes it.
//
// The version exists in the tree as prose in one place (the changelog heading)
// and as data in four (the registry manifest and three plugin manifests). That
// is a duplication the format forces: a plugin manifest states its own version
// at rest, because whatever installs it reads the file and not this repository.
//
// So the duplication cannot be removed, and the only remaining question is
// whether it can drift. It could, and did: the Claude Code plugin manifest sat
// at 0.0.0 through 0.0.5, and the Claude Desktop one still did when this was
// written. A stale version is valid JSON, passes every validator, and installs
// fine, so nothing anywhere failed.
//
// Two rules make it not drift again, and they only work together.
//
// One list, used by both the writer and the checker. `tools/version` stamps
// exactly what internal/hygiene asserts, so a manifest cannot be stamped and
// unchecked, or checked and unstamped.
//
// And the list itself is checked, by a test that goes looking for versioned
// manifests rather than trusting this file to be complete. A list of things to
// keep in sync is itself a thing that falls out of sync, which is how the
// Claude Desktop manifest stayed invisible: it was never in anybody's list.
package release

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Manifests state the release version as data, relative to the repository root.
//
// Adding one here is what puts it under the guard AND under the stamp. A new
// manifest that is not added fails TestNoVersionedManifestEscapesTheStamp,
// which is the point: the failure arrives when the file is added, not when a
// release quietly ships the wrong number in it.
var Manifests = []string{
	"server.json",
	"plugins/claude-code/.claude-plugin/plugin.json",
	"internal/plugins/data/claude-code/.claude-plugin/plugin.json",
	"plugins/claude-desktop/manifest.json",
}

// Changelog is where the version is stated as prose, and is the source of truth
// between releases: everything else is stamped from it.
const Changelog = "CHANGELOG.md"

// Unreleased is the heading that accumulates changes until a release claims it.
const Unreleased = "## [Unreleased]"

// released matches a version heading, newest first in a Keep a Changelog file.
var released = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)

// Current reports the newest released version stated in the changelog.
func Current(root string) (string, error) {
	body, err := os.ReadFile(filepath.Join(root, Changelog)) // #nosec G304 -- repository root plus a fixed filename
	if err != nil {
		return "", err
	}
	m := released.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("%s states no released version", Changelog)
	}
	return string(m[1]), nil
}

// Stamp claims the Unreleased section for version and writes that version into
// every manifest. It reports the files it changed.
//
// It does not commit and does not tag, deliberately. Tagging is the moment a
// release becomes real and is the owner's to perform; a tool that did it as a
// side effect of stamping would be deciding that on their behalf. Same shape as
// `dibs configure --service`, which writes the unit and prints the load command
// rather than running it.
func Stamp(root, version string) ([]string, error) {
	if err := validSemver(version); err != nil {
		return nil, err
	}
	current, err := Current(root)
	if err != nil {
		return nil, err
	}
	if newer, err := isNewer(version, current); err != nil {
		return nil, err
	} else if !newer {
		return nil, fmt.Errorf("%s is not newer than %s, which %s already records: a "+
			"release that goes backwards would leave every installer offering an older "+
			"build than the one before it", version, current, Changelog)
	}

	path := filepath.Join(root, Changelog)
	body, err := os.ReadFile(path) // #nosec G304 -- repository root plus a fixed filename
	if err != nil {
		return nil, err
	}
	text := string(body)
	if !strings.Contains(text, Unreleased) {
		return nil, fmt.Errorf("%s has no %q heading to release", Changelog, Unreleased)
	}
	// An empty section is a release with no notes, which is worse than no
	// release: the tag exists, the artifacts publish, and the one document that
	// says what changed says nothing. Refuse while it is still cheap to fix.
	if strings.TrimSpace(between(text, Unreleased, "## [")) == "" {
		return nil, fmt.Errorf("the %q section is empty: there is nothing to release, and "+
			"a version whose notes are blank is one nobody can decide whether to take",
			Unreleased)
	}

	// EVERY manifest is checked before ANY file is written.
	//
	// This wrote the changelog first and then rewrote manifests one at a time,
	// returning on the first failure. A manifest that is unwritable or missing
	// its version field therefore left the changelog stamped and some subset of
	// the manifests updated, and `Current` then reads the new version out of the
	// changelog, so the "must be newer" check below refuses the retry. The one
	// step before the tag would be half-done and unrepeatable, and repairing it
	// by hand is exactly what this command exists to stop anybody doing.
	//
	// A dry run first is the cheap version of a transaction: it cannot make the
	// write atomic, but it removes the failure that was actually reachable,
	// which is a file this tool can see is wrong before it has touched
	// anything. Found by a pre-release review, hours before I ran it.
	for _, rel := range Manifests {
		if _, err := setVersion(filepath.Join(root, rel), version, true); err != nil {
			return nil, fmt.Errorf("%s: %w\n\nNothing was written: every manifest is "+
				"checked before any of them is changed, so the tree is exactly as it "+
				"was", rel, err)
		}
	}

	// Keep an Unreleased heading above it: the next change has somewhere to go,
	// and its absence is how notes end up appended to a shipped version.
	stamped := fmt.Sprintf("%s\n\n## [%s] - %s", Unreleased, version,
		time.Now().Format("2006-01-02"))
	changed := []string{Changelog}
	// #nosec G703 -- path is the repository root joined to the Changelog
	// constant; no caller-supplied text reaches it.
	if err := os.WriteFile(path, []byte(strings.Replace(text, Unreleased, stamped, 1)), 0o600); err != nil {
		return nil, err
	}

	for _, rel := range Manifests {
		ok, err := setVersion(filepath.Join(root, rel), version, false)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", rel, err)
		}
		if ok {
			changed = append(changed, rel)
		}
	}
	return changed, nil
}

// maintains and reorder keys that read in a deliberate order, turning a
// one-line release stamp into a diff nobody can review.
// setVersion rewrites a manifest, or with dryRun only proves that it could.
//
// The dry pass exists so Stamp can check every manifest before it writes any of
// them. See the note there.
func setVersion(path, version string, dryRun bool) (bool, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- a manifest named in Manifests, joined to the repository root
	if err != nil {
		return false, err
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return false, fmt.Errorf("not readable as JSON: %w", err)
	}
	if doc.Version == version {
		return false, nil
	}
	old := fmt.Sprintf("%q: %q", "version", doc.Version)
	if !strings.Contains(string(body), old) {
		return false, fmt.Errorf("the version is not written as %s, so it cannot be "+
			"stamped without reformatting the file", old)
	}
	out := strings.Replace(string(body), old, fmt.Sprintf("%q: %q", "version", version), 1)
	// Reparsed rather than trusted: a textual edit that produced invalid JSON
	// would be discovered by whoever installed the release.
	if json.Unmarshal([]byte(out), &doc) != nil || doc.Version != version {
		return false, fmt.Errorf("stamping %s did not produce a manifest that reads back "+
			"as %s", path, version)
	}
	if dryRun {
		return true, nil // it would have worked, and nothing was touched
	}
	// #nosec G703 -- path is the repository root joined to an entry of
	// Manifests, which is a fixed list in this file.
	return true, os.WriteFile(path, []byte(out), 0o600)
}

// between returns the text from after start up to the next occurrence of stop.
func between(text, start, stop string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i+len(start):]
	if j := strings.Index(rest, stop); j >= 0 {
		return rest[:j]
	}
	return rest
}

func validSemver(v string) error {
	if _, err := parts(v); err != nil {
		return fmt.Errorf("%q is not a version: want MAJOR.MINOR.PATCH", v)
	}
	return nil
}

func parts(v string) ([3]int, error) {
	var out [3]int
	f := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(f) != 3 {
		return out, fmt.Errorf("want three fields, got %d", len(f))
	}
	for i, s := range f {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return out, fmt.Errorf("field %d is not a number", i+1)
		}
		out[i] = n
	}
	return out, nil
}

func isNewer(a, b string) (bool, error) {
	x, err := parts(a)
	if err != nil {
		return false, err
	}
	y, err := parts(b)
	if err != nil {
		return false, err
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i], nil
		}
	}
	return false, nil
}
