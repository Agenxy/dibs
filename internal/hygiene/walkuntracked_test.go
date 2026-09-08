package hygiene

import (
	"os"
	"path/filepath"
	"testing"
)

// The hygiene walk must see a file that has not been committed yet.
//
// Every check in this package is built on walk, and walk listed TRACKED files
// only. So a brand new file, which is the one most likely to break a
// convention because nothing about it has ever been reviewed, was the one file
// none of these guards read.
//
// The failure is quiet and it completes: write the file, run `task ci`, watch
// it pass having checked none of it, commit. The guard then fires on the NEXT
// run, against code that already shipped. That is how two em dashes in a new
// test file went through a green gate in this repository and were reported by
// the following one, one commit too late to be prevention.
//
// This is deliberately a test of the WALK and not of any one rule. The em-dash
// check happened to be what noticed; the hole belonged to every check equally,
// and a regression test written against em dashes would go green the moment
// somebody reorganised that one rule.
// An unreadable tracked file must FAIL the walk, not be counted as checked.
//
// The walk counted every callback it invoked and described that as "what was
// actually opened". Most callbacks return silently when ReadFile fails, so a
// file nothing could read still counted toward the minimum-files floor while no
// check examined a byte of it: the guard reporting coverage it does not have.
// Found by the pre-release review, one round after the untracked-files fix
// landed in this same function.
func TestTheWalkRefusesAFileItCannotRead(t *testing.T) {
	root := repoRoot(t)
	name := "zz_hygiene_unreadable_probe.md"
	abs := filepath.Join(root, name)
	if _, err := os.Stat(abs); err == nil {
		t.Fatalf("%s already exists: this probe would be reading somebody else's file", name)
	}
	// A REAL file with no read permission. A dangling symlink was the first
	// attempt and is filtered earlier, by the Stat check that skips "deleted but
	// still staged": it never reaches the read at all, so it tested nothing.
	if err := os.WriteFile(abs, []byte("unreadable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(abs, 0o600); _ = os.Remove(abs) })
	if err := os.Chmod(abs, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	if _, err := os.ReadFile(abs); err == nil {
		t.Skip("this process can read a 0000 file, which means it is running as " +
			"root: the case cannot be produced here")
	}

	// The walk must report it. Run it against a throwaway T so this test can
	// assert on the failure rather than inherit it.
	sub := &testing.T{}
	func() {
		defer func() { _ = recover() }()
		walk(sub, root, func(string, string) {})
	}()
	if !sub.Failed() {
		t.Error("the walk accepted a tracked file it could not read. It counts " +
			"toward the floor that proves the walk looked at something, while no " +
			"check in this package examined it")
	}
}

func TestTheHygieneWalkSeesUntrackedFiles(t *testing.T) {
	root := repoRoot(t)

	// A name git cannot already know about, in a directory that is definitely
	// walked. Removed on the way out whatever happens, so a failure here does
	// not leave litter that the NEXT run reports as a violation.
	name := "zz_hygiene_walk_probe_untracked.md"
	abs := filepath.Join(root, name)
	if _, err := os.Stat(abs); err == nil {
		t.Fatalf("%s already exists: this probe would be reading somebody else's "+
			"file and could delete it", name)
	}
	if err := os.WriteFile(abs, []byte("untracked probe\n"), 0o600); err != nil {
		t.Fatalf("writing the probe file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(abs) })

	var seenProbe, seenTracked bool
	walk(t, root, func(rel, _ string) {
		switch rel {
		case name:
			seenProbe = true
		case "AGENTS.md":
			seenTracked = true
		}
	})

	// The tracked half asserts the probe itself works: if walk visited nothing
	// at all, seenProbe would be false for a reason that has nothing to do with
	// tracking, and this test would report the wrong bug.
	if !seenTracked {
		t.Fatal("the walk did not visit AGENTS.md, so it is not walking the " +
			"repository and the assertion below would be meaningless")
	}
	if !seenProbe {
		t.Errorf("the walk did not visit %s, which exists and is untracked. Every "+
			"hygiene check in this package is therefore blind to new files until "+
			"somebody commits them, which is one run after the point of catching "+
			"them", name)
	}
}
