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
// them to `dibs web`. Collapsing Unavailable into Declined would tell somebody
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
// mechanism: a `agents-presence` picked up from somewhere else on PATH would be
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
// IT USED TO SKIP, in both gate modes, which is why this is the decision rather
// than Check. Check refuses before reaching the branch when no helper is
// installed beside the test binary, and neither the ordinary run nor `-tags
// dibdev` arranges one: reverting Abandoned to Declined left every gate green.
// A skipped test is not a weaker test, it is an absent one, and this was the
// only thing protecting the distinction.
func TestACancelledCallerIsAbandonedNotDeclined(t *testing.T) {
	failed := errors.New("the helper process died")
	cancelled := context.Canceled
	expired := context.DeadlineExceeded

	if v, _ := verdictFor(failed, cancelled, nil); v != Abandoned {
		t.Errorf("a cancelled caller is reported as %v, want Abandoned. Nobody was "+
			"asked, so nothing can be said about what they wanted", v)
	}
	// The caller's cancellation outranks our own deadline: both are set when a
	// cancelled call also runs out the clock, and only one of them is a decision
	// about a person.
	if v, _ := verdictFor(failed, cancelled, expired); v != Abandoned {
		t.Errorf("a cancelled caller whose deadline also passed is reported as %v, "+
			"want Abandoned", v)
	}
	// Our deadline alone IS a decline: a prompt really appeared and nobody
	// answered it.
	if v, _ := verdictFor(failed, nil, expired); v != Declined {
		t.Errorf("an unanswered prompt is reported as %v, want Declined", v)
	}
	// And a clean run is still a person.
	if v, _ := verdictFor(nil, nil, nil); v != Verified {
		t.Errorf("a successful check is reported as %v, want Verified", v)
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

// One presence prompt waits at a time, whatever asked for it.
//
// The defence against an agent obtaining the operator's approval is a sentence
// on the system sheet asking them to decline a prompt they did not cause. That
// cannot hold while two are waiting: they approve the one they expected, and
// the credential goes to whichever request the race picked. The person did
// nothing wrong and cannot see the difference.
//
// HERE, not in a caller. The first version of this serialised inside the
// board's `/bootstrap` handler, which covered that one door while `human_unlock`
// over MCP called Check directly and could overlap it, or overlap another
// unlock, with a reason line the requesting agent influences. The prompt is the
// shared thing: one person, one Mac, one sheet.
//
// It does NOT bind the approval to the requester. Nothing available here can,
// and SECURITY.md says so. It removes the silent case.
func TestOnlyOnePresencePromptWaitsAtATime(t *testing.T) {
	if !claimPrompt() {
		t.Fatal("the slot was already held with nothing waiting, so this test is " +
			"measuring leftover state rather than the rule")
	}
	defer releasePrompt()

	// The real entry point, because a test of claimPrompt alone would pass
	// against a Check that never consults it: that is exactly how the first
	// version of this covered one caller and not the other.
	if _, err := Check(context.Background(), "a second sheet"); !errors.Is(err, ErrPromptBusy) {
		t.Errorf("Check raised a second prompt while one was waiting (err=%v). One "+
			"approval would then satisfy whichever request the race picked, so an "+
			"agent can take a credential from a sheet the operator raised for "+
			"something else", err)
	}

	releasePrompt()
	if !claimPrompt() {
		t.Error("the slot was not released, so one finished prompt leaves this " +
			"machine unable to raise another until the daemon restarts")
	}
}
