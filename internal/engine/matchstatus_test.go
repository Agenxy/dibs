package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/overlap"
)

// A repository whose permissions recover must stop being reported unreadable.
//
// Unreadable entries survive a phase change on purpose: one tree finishing its
// index says nothing about another tree the daemon still cannot read. But the
// preserve kept the whole list, and no production caller ever sends the empty
// slice that clears it, so the only thing that could retract the diagnosis was
// something nothing does. The operator was told about a permissions problem
// that had been fixed, until the daemon restarted. Raised by the pre-release
// review, which reproduced it against the production sequence.
func TestARecoveredTreeStopsBeingReportedUnreadable(t *testing.T) {
	e := &Engine{}
	// The failure records the AGENT'S CWD, which is what discovery was given.
	// The recovery reports the repository ROOT it resolved to. Using the same
	// path for both, as the first version of this did, tests string equality
	// against itself and passes while the production transition is broken.
	e.SetMatchStatus(MatchStatus{
		Phase: MatchReady, Repo: "/a/subdir",
		Unreadable: []string{"/a/subdir", "/b"},
	})

	// /a indexes successfully, the way the scorer reports it: no Unreadable field.
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/a", Files: 10})

	got := e.matchStatus.st.Unreadable
	for _, tree := range got {
		if tree == "/a/subdir" {
			t.Errorf("a tree that just indexed successfully is still reported unreadable: %v", got)
		}
	}
	// And /b, which nothing has proved anything about, is untouched.
	var keptB bool
	for _, tree := range got {
		if tree == "/b" {
			keptB = true
		}
	}
	if !keptB {
		t.Errorf("one tree's success cleared another tree's failure: %v", got)
	}
}

// A FAILED retry must not erase the diagnosis it is reporting.
//
// Scorer failure paths publish MatchOff with a repository and no Unreadable
// field. Treating every nil Unreadable as proof that repository recovered
// removed the entry while publishing the failure, so the board could later read
// ready with no structured record that matching for that tree never came back.
// The first version of this fix covered a successful index and an unrelated
// repository, and not the transition that actually happens. Raised by the
// pre-release review.
func TestAFailedRetryKeepsTheUnreadableRecord(t *testing.T) {
	e := &Engine{}
	e.SetMatchStatus(MatchStatus{
		Phase: MatchReady, Repo: "/a", Unreadable: []string{"/a"},
	})

	// The retry fails: off, same repository, nothing said about readability.
	e.SetMatchStatus(MatchStatus{Phase: MatchOff, Repo: "/a"})

	var kept bool
	for _, tree := range e.matchStatus.st.Unreadable {
		if tree == "/a" {
			kept = true
		}
	}
	if !kept {
		t.Error("a failed retry erased the unreadable record for the tree it was " +
			"reporting on, so nothing is left saying matching never recovered there")
	}
}

// The DEFAULT success phase must clear an unreadable entry too.
//
// The recovery above keyed on MatchReady, and the test above hard-codes it, so
// both passed while the production default did not work. `ready` is the phase
// only when a join threshold is configured; the shipped default threshold is
// zero, which reports `suggest-only`, and a sidecar fallback reports
// `degraded`. Every ordinary board therefore kept telling its operator about a
// permissions problem it had already re-read successfully, until restart.
// Found by the pre-release review, which named the hard-coded phase as the
// reason the first guard could not catch it.
func TestARecoveredTreeIsClearedOnTheDefaultSuccessPhase(t *testing.T) {
	for _, phase := range []MatchPhase{MatchNoThreshold, MatchDegraded} {
		t.Run(string(phase), func(t *testing.T) {
			e := &Engine{}
			e.SetMatchStatus(MatchStatus{
				Phase: MatchReady, Repo: "/a/subdir",
				Unreadable: []string{"/a/subdir", "/b"},
			})
			e.SetMatchStatus(MatchStatus{Phase: phase, Repo: "/a", Files: 10})

			for _, tree := range e.matchStatus.st.Unreadable {
				if tree == "/a/subdir" {
					t.Errorf("a tree indexed successfully as %q is still reported "+
						"unreadable: %v", phase, e.matchStatus.st.Unreadable)
				}
			}
		})
	}

	// And a genuine failure still does not erase the diagnosis it is reporting.
	e := &Engine{}
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/a", Unreadable: []string{"/a"}})
	e.SetMatchStatus(MatchStatus{Phase: MatchOff, Repo: "/a"})
	if len(e.matchStatus.st.Unreadable) == 0 {
		t.Error("a failing repository erased its own unreadable entry, so the board " +
			"reports nothing wrong while matching for that tree is off")
	}
}

// A second repository failing must not switch matching off for the board.
//
// The unreadable-tree and indexing-tree routes were fixed; the ordinary
// mining and listing failures in the scorer still replaced the whole global
// status with `off`. The first repository's scorer is still installed and
// still answering, so the board annotated declarations with "matching is off"
// while matching worked: a claim the same daemon's behaviour contradicts.
// Found by the pre-release review, which pointed out that no test drove a
// working repository followed by an ordinary second-repository failure, which
// is exactly the production sequence.
func TestASecondRepositoryFailingDoesNotSwitchMatchingOff(t *testing.T) {
	e := &Engine{}
	// /a indexes, the way the default production path reports it, AND installs
	// a scorer. Installing one is the whole point: the round-six version of
	// this test claimed a scorer stayed installed and never called
	// SetScorerForRepo, so it proved only that two path strings differ. The
	// pre-release review named that as the reason it could not see the real
	// failure.
	e.SetScorerForRepo("/a", stubScorer{}, MatchConfig{})
	e.SetMatchStatus(MatchStatus{Phase: MatchNoThreshold, Repo: "/a", Files: 10})

	// /b fails the way bringUp does for an ordinary mining or listing error.
	e.SetMatchStatus(MatchStatus{
		Phase: MatchOff, Repo: "/b",
		Hint: "matching is off: mining co-change (exit status 128)",
	})

	if got := e.matchStatus.st.Phase; got == MatchOff {
		t.Errorf("phase = %q: one repository failing switched matching off for the "+
			"whole board, while /a's scorer is still installed and still "+
			"producing results", got)
	}
	var named bool
	for _, tree := range e.matchStatus.st.Unreadable {
		if tree == "/b" {
			named = true
		}
	}
	if !named {
		t.Errorf("the failing repository is not named anywhere (%v), so the operator "+
			"is told nothing at all about it: quietly dropping the failure is the "+
			"other half of this defect",
			e.matchStatus.st.Unreadable)
	}

	// And the FIRST repository failing still switches matching off, because
	// then there is nothing left working to speak for.
	e2 := &Engine{}
	e2.SetMatchStatus(MatchStatus{Phase: MatchNoThreshold, Repo: "/a", Files: 10})
	e2.SetMatchStatus(MatchStatus{Phase: MatchOff, Repo: "/a", Hint: "gone"})
	if e2.matchStatus.st.Phase != MatchOff {
		t.Errorf("phase = %q: the only working repository failed and the board still "+
			"reports matching as working", e2.matchStatus.st.Phase)
	}

	// The case the last-status-repository proxy got wrong: the SAME tree
	// brought up twice at once, which a prewarm plus a registration does. One
	// installs the scorer, the other fails, the paths match, and comparing
	// paths concluded there was nothing left to protect. There was: the scorer
	// it just installed, which goes on answering.
	e3 := &Engine{}
	e3.SetScorerForRepo("/a", stubScorer{}, MatchConfig{})
	e3.SetMatchStatus(MatchStatus{Phase: MatchNoThreshold, Repo: "/a", Files: 10})
	e3.SetMatchStatus(MatchStatus{Phase: MatchOff, Repo: "/a", Hint: "transient"})
	if e3.matchStatus.st.Phase == MatchOff && len(e3.IndexedRepos()) > 0 {
		t.Errorf("phase = off with %v still indexed: a duplicate bring-up of a tree "+
			"that is working switched the board off while its own scorer answers",
			e3.IndexedRepos())
	}
}

// stubScorer is an installed index that answers nothing. What matters here is
// that it IS installed: the decision under test is "does any tree still serve",
// and a scorer that returns no prediction is still a scorer that is serving.
type stubScorer struct{}

func (stubScorer) ID() string      { return "stub" }
func (stubScorer) Version() string { return "0" }
func (stubScorer) Predict(context.Context, string, int) (overlap.Prediction, error) {
	return overlap.Prediction{}, nil
}
