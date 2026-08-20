package engine

import "testing"

// One agent's unreadable directory is not the board's problem.
//
// A single agent registering from a tree macOS will not let the daemon read
// used to set the GLOBAL phase to off, replacing a working index for every
// other repository with "matching is off". Reported by an agent that had lost
// the feature fleet-wide and traced it correctly; the operator had spent the
// day seeing matching reported as off with a hint pointing at one directory.
func TestAnUnreadableTreeDoesNotSwitchMatchingOffForEverybody(t *testing.T) {
	e := &Engine{}
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/work/api", Files: 900})

	e.NoteUnreadableTree("/private/vault", "grant Documents access")

	got := e.MatchStatus()
	if got.Phase != MatchReady {
		t.Errorf("phase is %q after ONE unreadable tree; four working repositories "+
			"lose matching because a fifth agent started somewhere unreadable", got.Phase)
	}
	if len(got.Unreadable) != 1 || got.Unreadable[0] != "/private/vault" {
		t.Errorf("the unreadable tree is not named, so nobody can fix it: %v", got.Unreadable)
	}
	// Named once, however many agents start there.
	e.NoteUnreadableTree("/private/vault", "grant Documents access")
	if len(e.MatchStatus().Unreadable) != 1 {
		t.Error("the same tree was recorded twice")
	}
}

// With nothing working, the two statements coincide and off is the honest one.
func TestWithNothingIndexedAnUnreadableTreeIsTheWholeStory(t *testing.T) {
	e := &Engine{}
	e.NoteUnreadableTree("/private/vault", "grant Documents access")
	got := e.MatchStatus()
	if got.Phase != MatchOff {
		t.Errorf("phase is %q with nothing indexed at all; off is the truth here", got.Phase)
	}
	if got.Hint == "" {
		t.Error("no hint, so the operator has the fault and not the fix")
	}
}

// And a new tree starting to index does not demote a board that already works.
func TestIndexingANewTreeDoesNotDemoteAWorkingBoard(t *testing.T) {
	e := &Engine{}
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/work/api", Files: 900})
	e.NoteIndexingTree("/work/other")
	if got := e.MatchStatus().Phase; got != MatchReady {
		t.Errorf("phase is %q while a NEW tree indexes: an agent declaring in that "+
			"window is told matching is not ready when it is", got)
	}
	// From cold, it is genuinely indexing.
	fresh := &Engine{}
	fresh.NoteIndexingTree("/work/api")
	if got := fresh.MatchStatus().Phase; got != MatchIndexing {
		t.Errorf("a cold board reports %q rather than indexing", got)
	}
}

// One tree finishing its index must not erase what is wrong with another.
//
// SetMatchStatus replaced the whole status, and scorer completion calls it with
// no Unreadable value, so any repository reaching `ready` silently cleared the
// record of every other tree the daemon could not read. The operator was told
// about a permissions problem once, and a minute later told nothing, by a
// success somewhere unrelated. Found by a pre-release review.
func TestAReadyScorerDoesNotEraseUnreadableTrees(t *testing.T) {
	e, _, cancel := runningEngine(t)
	defer cancel()

	e.NoteUnreadableTree("/work/locked", "macOS will not let the daemon read it")
	if got := len(e.MatchStatus().Unreadable); got != 1 {
		t.Fatalf("setup: %d unreadable trees, want 1", got)
	}

	// Another repository finishes indexing, and says nothing about unreadable
	// trees because it knows nothing about them.
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/work/other"})

	st := e.MatchStatus()
	if st.Phase != MatchReady {
		t.Errorf("phase = %q, want ready", st.Phase)
	}
	if len(st.Unreadable) != 1 {
		t.Fatalf("the unreadable tree was erased by an unrelated repository "+
			"finishing: the operator is told a permissions problem exists and then, "+
			"without fixing anything, told it does not. Got %v", st.Unreadable)
	}

	// A caller that genuinely means "none" can still say so.
	e.SetMatchStatus(MatchStatus{Phase: MatchReady, Repo: "/work/other", Unreadable: []string{}})
	if got := len(e.MatchStatus().Unreadable); got != 0 {
		t.Errorf("an explicit empty list did not clear them: %d left", got)
	}
}
