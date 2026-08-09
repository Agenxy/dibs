package core

import (
	"strings"
	"testing"
)

// Evidence gathered without a known repository says so.
//
// SameRepo is false only on POSITIVE evidence of different trees, so unknown
// reads as true — deliberately, since treating it as foreign would disable
// matching for every client that reports no cwd. The cost was that the one line
// an agent acts on looked identical whether the shared repository was established
// or merely not disproved.
//
// A reviewer connecting over plain HTTP sent no cwd while the daemon happened to
// be indexed against a different project. It was shown that project's files as
// shared evidence, with same_repo true and repo_known false sitting side by side,
// and read the pair as a contradiction. Everything the evidence rests on is
// repository-scoped: pr:42 in one project has nothing to do with pr:42 in
// another.
func TestUnverifiedRepositoryIsSaidOutLoud(t *testing.T) {
	unknown := Evidence{
		SameRepo: true, RepoKnown: false,
		SurfaceInferred: []string{"runtime/src/k7d/main.rs"},
	}
	got := unknown.Strongest()
	if !strings.Contains(got, "unverified") {
		t.Errorf("evidence with an unknown repository does not say so: %q", got)
	}
	if !strings.Contains(got, "runtime/src/k7d/main.rs") {
		t.Errorf("the qualifier replaced the evidence instead of qualifying it: %q", got)
	}

	// A KNOWN shared repository must stay clean — a caveat on every line would
	// be noise, and noise is how a real caveat stops being read.
	known := Evidence{
		SameRepo: true, RepoKnown: true,
		SurfaceInferred: []string{"internal/core/evidence.go"},
	}
	if k := known.Strongest(); strings.Contains(k, "unverified") {
		t.Errorf("verified same-repo evidence carries an unnecessary caveat: %q", k)
	}

	// And positive evidence of DIFFERENT repositories is already a complete
	// answer; qualifying it would muddle a verdict with a doubt.
	diff := Evidence{SameRepo: false, RepoKnown: true}
	if d := diff.Strongest(); d != "different repositories" {
		t.Errorf("the different-repositories verdict was altered: %q", d)
	}
}
