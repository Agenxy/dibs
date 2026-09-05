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

// A value that is not a version must be refused before it becomes an argument.
//
// The explicit version went positionally to `gh release view`, and anything
// starting with a dash is not positional: `--help` was parsed by gh as an
// OPTION, exited zero, and the release-existence check read that as "the
// release exists". The manifest was stamped and the tool printed "publishing
// version --help". The check was real; its input was not. Found by the
// pre-release review.
func TestAVersionThatIsNotAVersionIsRefused(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "agenxy/dibs")
	for _, bad := range []string{"--help", "-h", "--repo", "latest", "", "1.2", "v"} {
		if bad == "" {
			continue // empty means "whatever is released", handled elsewhere
		}
		if _, err := resolve(bad); err == nil {
			t.Errorf("%q was accepted as a version to publish to the public registry", bad)
		}
	}
}

// And ordinary versions, including prereleases, still resolve.
func TestOrdinaryVersionsStillResolve(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	for _, good := range []string{"1.2.3", "v1.2.3", "0.0.7", "1.2.3-rc.1"} {
		if _, err := resolve(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
}
