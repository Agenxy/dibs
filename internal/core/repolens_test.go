package core

import "testing"

// lens is a stand-in for Git's answer. The point of these tests is the ADOPTION
// — that core asks, believes a known answer, and correctly does not treat
// silence as a denial — so a real repository here would only be testing
// internal/paths a second time.
type lens struct{ same, known bool }

func (l lens) SameRepo(_, _ string) (bool, bool) { return l.same, l.known }

// Everything sameRepo can do without Git is inference from the SHAPE of two
// paths, and shape gets both interesting cases wrong. A linked worktree lives
// wherever it was created — routinely outside its checkout — and is the same
// repository; two clones of unrelated projects sit side by side under one
// parent and are not. Git knows; the prefix test guesses.
func TestGitOutranksPathShape(t *testing.T) {
	// Two directories with nothing in common as strings, in one repository.
	// Without a lens this is the "different paths outside any known root" case,
	// which declines to commit — so an identifier could never act on it.
	same, known := sameRepo("/Users/x/proj", "/tmp/wt/feature", "", lens{true, true})
	if !same || !known {
		t.Errorf("linked worktree: got (%v,%v), want (true,true)", same, known)
	}

	// And the reverse: nesting is not membership. A prefix test says these are
	// certainly together, and it is certainly wrong when the inner directory is
	// a clone of something else.
	same, known = sameRepo("/work/outer/vendor/dep", "/work/outer", "", lens{false, true})
	if same || !known {
		t.Errorf("nested clone: got (%v,%v), want (false,true) — a veto", same, known)
	}
}

// "Git is not installed" and "these are different repositories" must never
// produce the same outcome. The first should change nothing; the second vetoes.
// Collapsing them would disable matching for everyone without Git, which looks
// exactly like Lanes being broken.
func TestNoGitAnswerChangesNothing(t *testing.T) {
	for _, l := range []RepoLens{nil, lens{false, false}, lens{true, false}} {
		// Same configured repository: still together, still on evidence.
		if same, known := sameRepo("/repo/cli", "/repo/docs", "/repo", l); !same || !known {
			t.Errorf("lens %v: in-repo pair got (%v,%v), want (true,true)", l, same, known)
		}
		// One inside the indexed tree and one outside: still a veto.
		if same, known := sameRepo("/repo/cli", "/elsewhere", "/repo", l); same || !known {
			t.Errorf("lens %v: split pair got (%v,%v), want (false,true)", l, same, known)
		}
		// Nothing to go on: unknown, and deliberately NOT a veto.
		if same, known := sameRepo("/work/a", "/work/b", "", l); !same || known {
			t.Errorf("lens %v: unrelated pair got (%v,%v), want (true,false)", l, same, known)
		}
	}
}

// An agent that reported no cwd has told us nothing about where it is, and must
// not be interrogated as though it had. This is checked because the lens is
// keyed by directory: an empty key is a directory that exists in no map.
func TestAnAbsentLocationIsNeverEvidence(t *testing.T) {
	for _, l := range []RepoLens{nil, lens{false, true}, lens{true, true}} {
		if same, known := sameRepo("", "/repo", "/repo", l); !same || known {
			t.Errorf("lens %v: got (%v,%v), want (true,false)", l, same, known)
		}
	}
}
