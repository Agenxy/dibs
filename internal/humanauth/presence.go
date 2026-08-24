// Package humanauth proves a HUMAN is at this machine, right now.
//
// Dibs' panel runs inside an agent's MCP host and acts with that agent's own
// token. That is fine for answering the agent's own mail: the agent handed the
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
// equally read the agent tokens, the ledger and the local secret, so presence is
// not the weakest thing it defeats, but the earlier claim here, that software
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
	"sync"
	"time"
)

// Verdict is what a presence check concluded.
type Verdict int

const (
	// Verified means a human authenticated just now.
	Verified Verdict = iota
	// Declined means a human was asked and did not authenticate: they dismissed
	// the sheet, failed the match, or let it time out. Distinct from Unavailable
	// because the remedies are opposite: try again, versus stop asking and use the
	// password. Distinct from Abandoned because a person actually saw a prompt.
	Declined
	// Unavailable means this machine cannot check biometrics at all.
	Unavailable
	// Abandoned means the request went away before a human answered: the caller
	// cancelled, or the daemon is shutting down.
	//
	// Distinct from Declined because Declined is a STATEMENT ABOUT A PERSON: it
	// says somebody was asked and said no, and the panel answers it with "nothing
	// was sent: press the button again when you want to act". Nobody was asked
	// here. Reporting a cancelled request as a refusal attributes a decision to a
	// human who may never have seen a prompt, in the one package whose entire
	// purpose is not to claim things about people that did not happen.
	Abandoned
)

// ErrNoHelper reports that the presence helper is not installed beside the
// daemon. Treated as Unavailable by Check, and named separately because
// "biometrics are off" and "Dibs was packaged without its helper" are different
// things for whoever has to fix it.
var ErrNoHelper = errors.New("presence helper not found beside dibd")

// promptTimeout bounds the system sheet. Longer than a person needs and shorter
// than a forgotten prompt holding a request open: the helper also caps itself,
// so this is the outer of two bounds rather than the only one.
const promptTimeout = 90 * time.Second

// helperName is the compiled Swift binary dibd execs.
//
// It said "agents-presence" while `task presence` built and installed
// "dibs-presence", so findHelper looked for a file that has never existed on
// any machine, and Check answered Unavailable every time. Touch ID, the one
// assertion in Dibs that must not be forgeable by software, was silently off
// from the rename until this was found: the product said "this build ships
// without the presence helper", which reads as a packaging decision rather than
// a typo, so nobody looked.
//
// Three spellings were in play, which is how it happened: lanes-presence (v1),
// agents-presence (the intermediate rename), dibs-presence (what ships).
// TestThePresenceHelperIsTheOneThatGetsBuilt now pins this to the Taskfile. A separate process
// rather than cgo because Dibs ships CGO_ENABLED=0 and cross-compiles to four
// targets: linking LocalAuthentication into the daemon would break every build
// that is not macOS. Exec also means a missing or unrunnable helper degrades to
// the password path instead of taking the daemon down with it.
const helperName = "dibs-presence"

// Check asks for proof that a human is present, showing them `reason`.
//
// The reason is displayed inside the system sheet, so it is the one chance to
// say what is being approved. Callers pass the actual action ("post to the agent
// auth-work") rather than a generic sentence.
func Check(ctx context.Context, reason string) (Verdict, error) {
	// ONE SHEET AT A TIME, and HERE rather than in a caller.
	//
	// The premise of the warning written on the sheet is that the operator can
	// decline a prompt they did not cause. That fails while two are waiting:
	// they approve the one they expected and the credential goes to whichever
	// request the race picked. Serialising was added to `/bootstrap` first and
	// covered exactly that one caller, while `human_unlock` over MCP called
	// this function directly and could still overlap it, or overlap another
	// unlock, with a reason line the requesting agent influences.
	//
	// The lock lives with the prompt because the prompt is the shared thing.
	// A per-caller version is a list of the callers somebody thought of, which
	// is how this was wrong the first time.
	//
	// It does NOT bind the approval to the requester; nothing here can, and
	// SECURITY.md says so. It removes the silent case.
	if !claimPrompt() {
		return Declined, ErrPromptBusy
	}
	defer releasePrompt()
	// A scripted verdict, in dev builds only: see mock_release.go. Consulted
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
	// point (see findHelper). `reason` is an argument, never a shell string,
	// there is no shell here.
	cmd := exec.CommandContext(ctx, helper, reason)
	err = cmd.Run()
	return verdictFor(err, parent.Err(), ctx.Err())
}

// verdictFor turns what happened into what may be said about the person.
//
// SPLIT FROM Check so a test can reach it, which is the same reason helperIn
// exists a few lines down and for the same failure: the only test protecting
// the Abandoned branch called Check, Check refuses before it gets here when no
// helper is installed beside the binary, and no gate arranges one. It skipped
// in the ordinary run AND under -tags dibdev, so reverting this branch to
// Declined left every gate green. That is the third time this package has
// needed the split and the second time it was found by a review rather than by
// a red test.
//
// runErr is what the helper's process returned; parentErr and deadlineErr are
// the caller's context and our own timeout, in that order of precedence.
func verdictFor(runErr, parentErr, deadlineErr error) (Verdict, error) {
	if runErr == nil {
		return Verified, nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		// The helper's exit codes ARE the API: 1 asked-and-refused, 2 cannot ask.
		switch ee.ExitCode() {
		case 1:
			return Declined, nil
		case 2:
			return Unavailable, nil
		}
	}
	// The caller went away: not a decision, and not a broken machine.
	//
	// The VERDICT is the answer here, and the error return is reserved for "we
	// could not decide at all": returning the process's failure alongside
	// Abandoned would make every caller that checks err treat a known outcome as
	// a fault. That is why the two branches below discard runErr deliberately.
	if parentErr != nil {
		return Abandoned, nil //nolint:nilerr // the verdict is the answer; see above
	}
	// Our own deadline expired: a human who never answered, which IS a decline:
	// nothing was approved, and telling them their Mac cannot do this would be
	// false.
	if deadlineErr != nil {
		return Declined, nil //nolint:nilerr // the verdict is the answer; see above
	}
	return Unavailable, runErr
}

// findHelper looks beside the running binary, and refuses a symlink.
//
// Beside, not on PATH: the helper is part of this daemon's install, and picking
// up a `agents-presence` from somewhere else on PATH would mean trusting an
// unrelated binary to answer "is a human here". That is the one question where
// substituting the answerer defeats the whole mechanism.
//
// WHAT THIS DOES NOT DO, stated plainly because the surrounding comments used to
// overclaim it. The default install directory is ~/.local/bin, which is owned
// and writable by the user. Anything already running as that user can replace
// the helper, or, before this, point a symlink at something else, with a
// binary that exits 0, and Check would report Verified without a sensor ever
// being touched. So presence is unforgeable by a REMOTE or unprivileged caller,
// and by an agent confined to the MCP transport, which is the threat this was
// built for: an agent that wants to speak as the operator must raise a system
// sheet on the operator's own Mac. It is NOT unforgeable by arbitrary code
// already executing with the user's own privileges. That adversary can also
// read the agent tokens, the ledger and ~/.dibs/local.secret, so presence is not
// the weakest link, but "software cannot produce a fingerprint" was the wrong
// sentence and this one is the right one.
//
// The symlink refusal is a real narrowing rather than a fix: replacing the file
// in place still works, and defeating that needs a signature check or an install
// root the user cannot write. What it buys is that the cheapest version of the
// substitution (dropping a link next to the daemon) no longer succeeds
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
	// symlink named agents-presence pointing at /usr/bin/true satisfied every
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

// ErrPromptBusy is returned when a presence sheet is already waiting.
//
// A refusal rather than a queue: queueing is what lets one approval satisfy a
// request it was not raised for, which is the whole thing being prevented. The
// caller is expected to say "try again" rather than to wait.
var ErrPromptBusy = errors.New("a presence check is already waiting for an answer")

// promptBusy guards the single on-screen prompt WITHIN ONE DAEMON, which is
// less than the screen it is reasoning about.
//
// This said "package level because the SCREEN is package level: two authGates,
// or a gate and an MCP handler, are still one person looking at one Mac". The
// premise is right and the conclusion does not follow from it: a screen is
// MACHINE level, and a package-level mutex is per PROCESS. `dibd
// -allow-parallel` is a supported deployment of exactly several processes on
// one Mac, and each of them holds its own copy of this, so each can have a
// presence check waiting at the same time. The one thing the refusal exists to
// prevent, an approval satisfying a request it was not raised for, is
// prevented inside a daemon and not between them.
//
// NOT YET CLOSED, and said here rather than left as a guarantee that is not
// one. Whether it is reachable in practice depends on something this comment
// cannot assert: whether macOS serialises the LocalAuthentication sheets from
// two signed helpers, and whether a person can tell the two apart. That needs
// two release daemons with separate data directories and concurrent
// human_unlock calls, and until somebody runs it the honest statement is that
// Dibs does not provide the machine-wide control, rather than that it does.
//
// If it is closed, the shape already exists: parallel.go claims its daemon slot
// under a host-wide lock for the same reason, so that two daemons starting
// together cannot both conclude they are alone.
var promptBusy struct {
	sync.Mutex
	held bool
}

func claimPrompt() bool {
	promptBusy.Lock()
	defer promptBusy.Unlock()
	if promptBusy.held {
		return false
	}
	promptBusy.held = true
	return true
}

func releasePrompt() {
	promptBusy.Lock()
	defer promptBusy.Unlock()
	promptBusy.held = false
}
