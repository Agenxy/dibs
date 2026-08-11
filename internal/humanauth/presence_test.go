package humanauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A missing helper degrades to the password path rather than to a lie.
//
// The three verdicts are not severities, they are different sentences to say to
// a person: Verified acts, Declined says nothing was sent, and Unavailable sends
// them to `lanes web`. Collapsing Unavailable into Declined would tell somebody
// on a Mac with no sensor to try their finger again: advice that cannot work,
// which is this project's named failure mode.
func TestAMissingHelperIsUnavailableNotDeclined(t *testing.T) {
	// findHelper looks beside the running executable; the test binary lives in a
	// temp build directory with no helper next to it.
	if Available() {
		t.Skip("a presence helper is installed beside the test binary")
	}
	verdict, err := Check(t.Context(), "probe")
	if verdict != Unavailable {
		t.Errorf("verdict = %v, want Unavailable", verdict)
	}
	if !errors.Is(err, ErrNoHelper) {
		t.Errorf("err = %v, want ErrNoHelper: the caller needs to tell a missing "+
			"helper from a machine with no sensor", err)
	}
}

// The helper is taken from beside the daemon, never from PATH.
//
// This is the one question where substituting the answerer defeats the whole
// mechanism: a `lanes-presence` picked up from somewhere else on PATH would be
// an unrelated binary answering "is a human here", and it could answer yes.
func TestTheHelperIsNotTakenFromPATH(t *testing.T) {
	dir := t.TempDir()
	// An executable of the right name, in a directory that is on PATH but is not
	// beside this binary.
	planted := filepath.Join(dir, helperName)
	if err := os.WriteFile(planted, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("plant: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	found, err := findHelper()
	if err == nil && found == planted {
		t.Fatal("the helper was taken from PATH: anything on PATH could then " +
			"answer 'a human is present'")
	}
}

// A cancelled CALLER is not a human declining.
//
// Both surface as ctx.Err() on the derived context, and Check used to map either
// to Declined, so a client that disconnected, or a daemon shutting down, was
// recorded as a person who was asked and said no. The panel answers Declined with
// "press the button again when you want to act", which tells somebody they
// changed their mind about a prompt they may never have seen. In the one package
// whose purpose is not to claim things about people that did not happen, that was
// the wrong default.
//
// The distinction is the caller's context versus our own 90s deadline: the
// timeout IS a decline (nobody answered a prompt that really appeared), the
// cancellation is not.
func TestACancelledCallerIsAbandonedNotDeclined(t *testing.T) {
	if !Available() {
		t.Skip("no presence helper installed beside the test binary")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	verdict, _ := Check(ctx, "probe")
	if verdict == Verified {
		t.Fatal("a cancelled check reported a verified human")
	}
	if verdict == Declined {
		t.Error("a cancelled caller was reported as a human decline: nobody was asked, " +
			"so nothing can be said about what they wanted")
	}
	if verdict != Abandoned {
		t.Errorf("verdict = %v, want Abandoned", verdict)
	}
}

// A symlink named like the helper is not the helper.
//
// os.Stat follows links and reports on the target, so a symlink dropped beside
// the daemon and pointed at anything that exits 0 satisfied every check here and
// made Check return Verified with no sensor involved. "Beside, not on PATH" was
// true and insufficient: it constrained WHERE the answerer is found, not WHAT
// gets to answer.
//
// This narrows the cheapest substitution, not all of them: replacing the file
// in place still works, because the install directory belongs to the user. See
// findHelper's comment for what that does and does not buy.
func TestASymlinkIsNotAcceptedAsTheHelper(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "always-yes")
	if err := os.WriteFile(target, []byte("binary"), 0o700); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, helperName)); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	// helperIn is the PRODUCTION rule. An earlier version of this test ran its
	// own os.Lstat and asserted on the result, so it passed just as happily with
	// os.Stat back in the daemon: it was testing the standard library.
	if got, err := helperIn(dir); err == nil {
		t.Errorf("helperIn accepted a symlink at %s: a link named like the helper "+
			"and pointed at anything that exits 0 would answer 'a human is present'", got)
	}
}

// A real file in the same position IS accepted, so the rule above is a symlink
// refusal rather than a helper that can never be found.
func TestARealHelperIsStillAccepted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, helperName), []byte("binary"), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if _, err := helperIn(dir); err != nil {
		t.Errorf("helperIn rejected a real executable helper: %v: the symlink rule "+
			"must not have made the helper unfindable", err)
	}
}
