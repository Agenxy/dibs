package engine

import "testing"

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
