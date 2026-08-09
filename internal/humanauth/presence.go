// Package humanauth proves a HUMAN is at this machine, right now.
//
// Lanes' panel runs inside an agent's MCP host and acts with that agent's own
// token. That is fine for answering the agent's own mail — the agent handed the
// token over. It is not fine for speaking AS the operator. "Stand down, this is
// your operator" is exactly the sentence that must never be forgeable, and
// nothing in the transport can tell "the human clicked Broadcast" from "an agent
// called the tool": both arrive on the same connection with the same credential.
//
// So the proof has to come from outside the transport. Touch ID is the right
// primitive because an agent confined to that transport cannot produce it: one
// that tried to unlock would raise the system sheet on the human's own Mac, and
// the human would decline. Presence is verified rather than asserted, and the
// failure mode is a visible prompt rather than a silent escalation.
//
// The bound is the transport, and saying it precisely matters. This does not
// stop arbitrary code ALREADY running as the user, which can replace the helper
// binary in a directory the user owns and have it exit 0. That adversary can
// equally read the lane tokens, the ledger and the local secret, so presence is
// not the weakest thing it defeats — but the earlier claim here, that software
// cannot produce a fingerprint, was wrong and worth correcting rather than
// leaving as reassurance. See findHelper for exactly what is and is not bought.
//
// The password fallback is a real fallback, not a synonym. Biometrics are absent
// on Linux, on Macs without the sensor, and in a headless session; there the
// operator proves themselves with the admin password the board already uses. The
// two are distinguished all the way out to the caller so the panel can say which
// one it is asking for, rather than demanding a finger from a machine that has
// no reader.
package humanauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Verdict is what a presence check concluded.
type Verdict int

const (
	// Verified means a human authenticated just now.
	Verified Verdict = iota
	// Declined means a human was asked and did not authenticate — they dismissed
	// the sheet, failed the match, or let it time out. Distinct from Unavailable
	// because the remedies are opposite: try again, versus stop asking and use the
	// password. Distinct from Abandoned because a person actually saw a prompt.
	Declined
	// Unavailable means this machine cannot check biometrics at all.
	Unavailable
	// Abandoned means the request went away before a human answered — the caller
	// cancelled, or the daemon is shutting down.
	//
	// Distinct from Declined because Declined is a STATEMENT ABOUT A PERSON: it
	// says somebody was asked and said no, and the panel answers it with "nothing
	// was sent — press the button again when you want to act". Nobody was asked
	// here. Reporting a cancelled request as a refusal attributes a decision to a
	// human who may never have seen a prompt, in the one package whose entire
	// purpose is not to claim things about people that did not happen.
	Abandoned
)

// ErrNoHelper reports that the presence helper is not installed beside the
// daemon. Treated as Unavailable by Check, and named separately because
// "biometrics are off" and "Lanes was packaged without its helper" are different
// things for whoever has to fix it.
var ErrNoHelper = errors.New("presence helper not found beside lanesd")

// promptTimeout bounds the system sheet. Longer than a person needs and shorter
// than a forgotten prompt holding a request open: the helper also caps itself,
// so this is the outer of two bounds rather than the only one.
const promptTimeout = 90 * time.Second

// helperName is the compiled Swift binary lanesd execs. A separate process
// rather than cgo because Lanes ships CGO_ENABLED=0 and cross-compiles to four
// targets — linking LocalAuthentication into the daemon would break every build
// that is not macOS. Exec also means a missing or unrunnable helper degrades to
// the password path instead of taking the daemon down with it.
const helperName = "lanes-presence"

// Check asks for proof that a human is present, showing them `reason`.
//
// The reason is displayed inside the system sheet, so it is the one chance to
// say what is being approved. Callers pass the actual action ("post to the lane
// auth-work") rather than a generic sentence.
func Check(ctx context.Context, reason string) (Verdict, error) {
	// A scripted verdict, in dev builds only — see mock_release.go. Consulted
	// before the helper so the mock can also stand in for a machine that has one,
	// which is what makes the Declined and Unavailable branches testable at all.
	if verdict, on := mocked(); on {
		return verdict, nil
	}
	helper, err := findHelper()
	if err != nil {
		return Unavailable, err
	}
	// Keep the caller's context distinguishable from our own deadline. Both
	// surface as ctx.Err() on the derived context, and they mean opposite things:
	// our timeout is a human who did not answer in ninety seconds, the caller's
	// cancellation is a request that stopped existing.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, promptTimeout)
	defer cancel()

	// #nosec G204 -- `helper` is not caller input: findHelper resolves a fixed
	// filename beside this executable and refuses anything else, which is the
	// point (see findHelper). `reason` is an argument, never a shell string —
	// there is no shell here.
	cmd := exec.CommandContext(ctx, helper, reason)
	err = cmd.Run()
	if err == nil {
		return Verified, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		// The helper's exit codes ARE the API: 1 asked-and-refused, 2 cannot ask.
		switch ee.ExitCode() {
		case 1:
			return Declined, nil
		case 2:
			return Unavailable, nil
		}
	}
	// The caller went away: not a decision, and not a broken machine.
	if parent.Err() != nil {
		return Abandoned, nil
	}
	// Our own deadline expired — a human who never answered, which IS a decline:
	// nothing was approved, and telling them their Mac cannot do this would be
	// false.
	if ctx.Err() != nil {
		return Declined, nil
	}
	return Unavailable, err
}

// findHelper looks beside the running binary, and refuses a symlink.
//
// Beside, not on PATH: the helper is part of this daemon's install, and picking
// up a `lanes-presence` from somewhere else on PATH would mean trusting an
// unrelated binary to answer "is a human here". That is the one question where
// substituting the answerer defeats the whole mechanism.
//
// WHAT THIS DOES NOT DO, stated plainly because the surrounding comments used to
// overclaim it. The default install directory is ~/.local/bin, which is owned
// and writable by the user. Anything already running as that user can replace
// the helper — or, before this, point a symlink at something else — with a
// binary that exits 0, and Check would report Verified without a sensor ever
// being touched. So presence is unforgeable by a REMOTE or unprivileged caller,
// and by an agent confined to the MCP transport, which is the threat this was
// built for: an agent that wants to speak as the operator must raise a system
// sheet on the operator's own Mac. It is NOT unforgeable by arbitrary code
// already executing with the user's own privileges. That adversary can also
// read the lane tokens, the ledger and ~/.lanes/local.secret, so presence is not
// the weakest link — but "software cannot produce a fingerprint" was the wrong
// sentence and this one is the right one.
//
// The symlink refusal is a real narrowing rather than a fix: replacing the file
// in place still works, and defeating that needs a signature check or an install
// root the user cannot write. What it buys is that the cheapest version of the
// substitution — dropping a link next to the daemon — no longer succeeds
// silently, and Lstat cannot be satisfied by a path that resolves elsewhere.
func findHelper() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", ErrNoHelper
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	return helperIn(filepath.Dir(self))
}

// helperIn is the rule, separated from "which directory am I in" so a test can
// exercise it.
//
// The symlink test used to perform its own os.Lstat and assert on that, which
// tested the standard library rather than this package: reverting the production
// call to os.Stat left it green. A regression test that never calls the function
// it guards is the third instance of that pattern in this codebase's history and
// the reason this split exists.
func helperIn(dir string) (string, error) {
	candidate := filepath.Join(dir, helperName)
	// Lstat, not Stat: Stat follows the link and reports on the target, so a
	// symlink named lanes-presence pointing at /usr/bin/true satisfied every
	// condition here and answered "a human is present" forever after.
	info, serr := os.Lstat(candidate)
	if serr != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", ErrNoHelper
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrNoHelper
	}
	return candidate, nil
}

// Available reports whether a biometric check can even be attempted here, so a
// caller can offer the right thing rather than asking for a finger and then
// apologising.
func Available() bool {
	if Mocked() {
		// Including the mocked case is the point: a caller that offered the unlock
		// button only when a real sensor was found would make the mock unreachable
		// through the UI, which is the surface it exists to exercise.
		return true
	}
	_, err := findHelper()
	return err == nil
}
