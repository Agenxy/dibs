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
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/a", Unreadable: []string{"/a", "/b"}})

	// /a indexes successfully, the way the scorer reports it: no Unreadable field.
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/a", Files: 10})

	got := e.matchStatus.st.Unreadable
	for _, tree := range got {
		if tree == "/a" {
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
