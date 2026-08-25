package main

import (
	"strings"
	"testing"
)

// An explicit version must correspond to a real release.
//
// resolve's own comment says it answers "in the order that cannot invent one
// nobody shipped", and its first branch took any string verbatim: a manual
// dispatch with -version 9.9.9 stamped and published 9.9.9 to the public
// registry for a version that was never built, tagged or released. The release
// job's `needs:` was added to close exactly that, and this recovery path beside
// it walked around the guard while the changelog claimed it was shut. Found by
// the pre-release review, which reproduced it read-only.
//
// The registry is public and permanent, so a version published there that
// nobody can download is worse than a failed job: every client indexing from it
// then offers an install that cannot succeed.
func TestAnExplicitVersionMustBeReleased(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "agenxy/dibs")
	if _, err := resolve("99.99.99"); err == nil {
		t.Fatal("a version with no GitHub release was accepted for publication to " +
			"the public registry")
	} else if !strings.Contains(err.Error(), "99.99.99") {
		t.Errorf("the refusal does not name the version it refused: %v", err)
	}
}

// And a local run, where there is nothing to ask, still resolves: refusing
// there would make this tool untestable without a network while publishing
// nothing.
func TestALocalRunNeedsNoRelease(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	got, err := resolve("1.2.3")
	if err != nil {
		t.Fatalf("a local run was refused: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("resolved %q, wanted 1.2.3", got)
	}
}

// The leading v is still stripped, which is what every caller passes.
func TestALeadingVIsStripped(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	if got, _ := resolve("v4.5.6"); got != "4.5.6" {
		t.Errorf("resolved %q, wanted 4.5.6", got)
	}
}
