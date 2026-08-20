// Package notify reaches the HUMAN, on the machine, without their agent having
// to tell them.
//
// Everything else Dibs does is pull-shaped: an agent asks, the board answers.
// That is right for agents and wrong for the person, because the person is not
// in a loop. A request addressed to them sat on the board until they happened to
// look, and the operator's own summary of it was that a fleet which needs a
// human to notice is not much of a fleet.
//
// # Why osascript and not a Swift helper
//
// A passive banner needs a bundle identifier. `UNUserNotificationCenter`
// crashes without one, and `NSUserNotification` has been deprecated since
// macOS 11 and does nothing on current systems. osascript is itself a bundled,
// notification-entitled application, which is why every tool that posts a
// banner without shipping an app goes through it.
//
// Action BUTTONS on a banner are the part this cannot do. Those need a real
// application bundle, which is the natural next step and the reason the icon
// work matters; until then anything needing an answer is an alert, which does
// have buttons and does return which one was pressed.
//
// # The injection rule, which is the whole safety argument
//
// Every string here originates with an AGENT. Interpolating one into an
// AppleScript source would be handing an agent `do shell script`, so nothing is
// interpolated: the script is a constant with an `on run argv` handler and the
// text arrives as arguments. Verified with a body containing
// `" & (do shell script "…") & "`, which comes back as literal text.
//
// Never build a script with fmt.Sprintf in this file.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrUnsupported is returned where there is no way to reach a person. Dibs
// supports macOS and Linux; only the former has a route that needs no daemon,
// no bundle and no configuration.
var ErrUnsupported = errors.New("no desktop notification route on this platform")

// timeout bounds every prompt. An alert nobody answers must not hold a
// goroutine, or an unattended machine accumulates one per message.
const timeout = 2 * time.Minute

// windowTimeout bounds the escalated ask, which is a WINDOW rather than a
// banner and therefore stays put until somebody deals with it.
//
// Fifteen minutes, not two: a person who steps away from a window comes back to
// it, which is the entire reason this path exists. Still bounded, because a
// modal nobody ever answers is a process held open on an unattended machine and
// a dialog in the way of whatever they do next.
const windowTimeout = 15 * time.Minute

// banner posts a notification. Arguments, never interpolation.
const banner = `on run argv
  display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv)
end run`

// alert asks a question with up to three buttons and returns the one pressed.
const alert = `on run argv
  set n to count of argv
  set btns to items 3 thru n of argv
  set r to display dialog (item 2 of argv) with title (item 1 of argv) ¬
    buttons btns default button (item n of argv) with icon note
  return button returned of r
end run`

// prompt asks for free text.
const prompt = `on run argv
  set r to display dialog (item 2 of argv) with title (item 1 of argv) default answer "" with icon note
  return text returned of r
end run`

// pick offers an arbitrary number of choices, which a dialog cannot: macOS caps
// those at three buttons.
const pick = `on run argv
  set n to count of argv
  set opts to items 3 thru n of argv
  set r to choose from list opts with title (item 1 of argv) with prompt (item 2 of argv)
  if r is false then return ""
  return item 1 of r
end run`

// helperName is the bundled notifier, which is what gives a notification Dibs'
// own name and icon, and is the only route that carries action buttons.
const helperName = "Dibs.app/Contents/MacOS/dibs-notify"

// helper resolves the bundled notifier beside the running binary, the same rule
// the presence helper follows. Empty when this is a source build with no app.
func helper() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	path := filepath.Join(filepath.Dir(self), helperName)
	if st, err := os.Stat(path); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return ""
	}
	return path
}

// Banner shows a passive notification and returns at once.
//
// Through the bundle where there is one, because a notification carries the
// identity of whoever posted it: osascript lends it Script Editor's name and
// icon, which is what every message from an agent was branded with until Dibs
// had an application of its own.
func Banner(title, subtitle, body string) error {
	if h := helper(); h != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// #nosec G204 -- h is resolved beside this binary; the rest is data the
		// helper reads as argv.
		return exec.CommandContext(ctx, h, title, subtitle, body).Run()
	}
	_, err := run(banner, append([]string{title, subtitle}, body)...)
	return err
}

// Ask shows an alert with the given buttons and returns the one pressed. The
// last button is the default. Two or three choices; use Pick for more.
func Ask(title, body string, buttons ...string) (string, error) {
	if len(buttons) == 0 || len(buttons) > 3 {
		return "", errors.New("an alert takes one to three buttons; use Pick for more")
	}
	// The bundle puts the buttons ON the banner, which is the whole reason it
	// exists: the fallback has to interrupt with a modal alert to ask the same
	// question, and a coordination service that steals focus to ask an optional
	// question is worse than one that waits.
	if h := helper(); h != "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
		defer cancel()
		// A banner nobody can see is not an ask.
		//
		// Where a Focus mode is active AND this build cannot mark a notification
		// Time Sensitive, the banner is silenced by construction: it is delivered,
		// macOS holds it in Notification Centre, and the person finds it whenever
		// they next go looking, which for a request with a deadline is the same as
		// never. Measured exactly that way, three times in one evening, on a
		// request the operator had asked for twice.
		//
		// So escalate to a window, which Focus does not silence. Only then: a
		// service that steals focus for every question is one people turn off,
		// and that argument is why this is not the default.
		args := append([]string{title, "", body}, buttons...)
		// Logged because the CHOICE is the thing nobody could see afterwards.
		// The daemon recorded that it notified and never which channel it used,
		// so "I did not get it" had no evidence attached and three different
		// explanations that all looked identical from the outside.
		if silenced := bannersAreSilenced(); silenced {
			slog.Info("asking in a window", "why", "a focus mode is on and this build cannot mark a notification time-sensitive")
			return askInAWindow(h, title, body, buttons)
		}
		slog.Info("asking on a banner", "focus", focusOn())
		// #nosec G204 -- h is resolved beside this binary; the rest is argv data.
		out, err := exec.CommandContext(ctx, h, args...).Output()
		if err != nil {
			// Exit 2 means the machine WILL NOT notify: no authorisation, no
			// bundle. That is not a person deferring, and collapsing the two is
			// how a question nobody could see was reported as a question nobody
			// answered, while the asking agent waited out its deadline. The
			// helper documents the distinction; this end was throwing it away.
			// Found by an independent review before release.
			if notAuthorised(err) {
				return "", ErrCannotNotify
			}
			// EXIT 1 IS THE DISMISSAL. Everything else is the machine failing.
			//
			// This returned ("", nil) for every remaining error, and the helper
			// documents exactly three codes: 0 chose, 1 dismissed, 2 cannot
			// notify. A command that could not start, a crash, a signal, our own
			// context deadline, or any code the helper does not define came back
			// as "the person declined", so the asking agent waited out its
			// deadline against a banner that was never posted and no layer
			// reported a fault. The distinction was already written down one
			// file away, in the helper's own header. Found by a pre-release
			// review, in the branch added to fix the same class for exit 2.
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == 1 {
				return "", nil // a person, dismissing
			}
			return "", fmt.Errorf("%w: the notifier did not run to a documented "+
				"outcome: %v", ErrNoAnswer, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return run(alert, append([]string{title, body}, buttons...)...)
}

// Pick offers a list and returns the choice, or "" if dismissed.
func Pick(title, body string, choices ...string) (string, error) {
	if len(choices) == 0 {
		return "", errors.New("nothing to choose from")
	}
	if out, ok := onScreen("--pick", append([]string{title, body}, choices...)...); ok {
		return out, nil
	}
	return run(pick, append([]string{title, body}, choices...)...)
}

// Prompt asks for free text and returns it, or "" if dismissed.
func Prompt(title, body string) (string, error) {
	if out, ok := onScreen("--prompt", title, body); ok {
		return out, nil
	}
	return run(prompt, title, body)
}

// onScreen runs the half of answering that needs a window, through the bundle.
//
// The notification already comes from there, because only that API carries
// buttons and only a bundle carries an identity. The box that opens when
// somebody presses "Answer…" did not: it was an osascript `display dialog`, and
// a background LaunchAgent has no foreground application for a dialog to belong
// to. Pressing the button dismissed the notification, osascript ran, and nothing
// appeared. Reported exactly that way: "when I clicked answer it just went away,
// there was nowhere to put an answer."
//
// ok=false means there is no bundle here, and the osascript path is still the
// fallback: it works from a foreground process and this is a source build.
func onScreen(mode string, args ...string) (string, bool) {
	h := helper()
	if h == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// #nosec G204 -- h is resolved beside this binary; mode is a constant and
	// the rest is argv data the helper never interprets.
	out, err := exec.CommandContext(ctx, h, append([]string{mode}, args...)...).Output()
	if err != nil {
		if notAuthorised(err) {
			return "", false // fall through to the script path rather than lie
		}
		// EXIT 1 IS CANCEL. Anything else is the helper failing.
		//
		// This returned handled=true for every remaining error, so a helper that
		// could not start, crashed, took a signal, or hit our own deadline was
		// reported as the person cancelling: Prompt and Pick then hand back an
		// empty answer with no error, and the request is quietly left open. The
		// `Ask` path two functions away had exactly this and was corrected; this
		// one repeated it. Found by a pre-release review.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", true // cancelled, or sent empty: an answer
		}
		return "", false // the machine failed: let the caller try the script path
	}
	return strings.TrimSpace(string(out)), true
}

// Available reports whether a person can be reached from here at all, so a
// caller can say "this build cannot notify you" once rather than failing
// silently on every message.
func Available() bool { return runtime.GOOS == "darwin" && !underTest() && !silenced() }

// silenced reports the operator's kill switch for this process and everything
// it spawns. Split out so it is testable from inside a test binary, where
// Available() is already false for a different reason and would prove nothing.
func silenced() bool { return os.Getenv(silenceEnv) == "off" }

// silenceEnv turns every notification off for a process and everything it
// spawns.
//
// underTest() catches a test BINARY, and that is not enough: the end-to-end
// suites spawn a real dibd, which is a production binary by every measure and
// notifies accordingly. The operator watched fixture dialogs, "checker asks:
// Should I proceed with the rename?", appear on their screen on every test run,
// with buttons that answered a daemon about to be killed.
//
// Set on the test tasks in one place rather than at each of the five spawn
// sites: the suites pass their whole environment to the daemons they start, so
// one variable reaches all of them and a new suite inherits it without anybody
// remembering. A spawn site that forgets is exactly how this came back.
const silenceEnv = "DIBS_NOTIFY"

// underTest reports whether this is a test binary, in which case nothing here
// may reach a person.
//
// A test that notifies is not a test. `go test ./...` on this repository put
// real alerts on the operator's screen, branded with a generic icon because a
// test binary has no Dibs.app beside it to lend its identity, carrying fixture
// text ("make asker coordinator?", "promote me") and buttons that answered a
// process which had already moved on. They then reported the product as broken
// on the evidence of its own test suite, which is worse than the noise: it
// destroys the signal from the real thing.
//
// By binary name rather than by importing `testing`, which would pull the test
// flags into the daemon. `go test` builds `<pkg>.test`, and `go run` never
// produces that suffix.
func underTest() bool {
	self, err := os.Executable()
	return err == nil && strings.HasSuffix(filepath.Base(self), ".test")
}

func run(script string, args ...string) (string, error) {
	if !Available() {
		return "", ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// #nosec G204 -- `script` is one of the constants above and never built from
	// input; args are DATA delivered to `on run argv`, which is the entire
	// reason this file interpolates nothing.
	cmd := exec.CommandContext(ctx, "osascript", append([]string{"-e", script}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		// A dismissed dialog exits non-zero, which is an answer rather than a
		// fault: "the human said no" and "the machine could not ask" must not
		// look the same to the caller.
		//
		// THE PART THAT WAS MISSING: so does osascript failing outright. No GUI
		// session, automation permission refused, a script error, the binary
		// gone: every one of those is an ExitError too, and every one of them
		// came back as ("", nil), which the caller reads as a person choosing
		// not to answer. The agent then waits out its deadline against a dialog
		// that was never drawn, and nothing anywhere says so. That is the exact
		// failure this file was written to remove, surviving in the branch that
		// exists to remove it. Found by a pre-release review, one path along
		// from the same defect in askInAWindow.
		//
		// A cancel is AppleScript error -128, and osascript writes it to stderr.
		// Anything else is the machine failing, not the person declining.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if bytes.Contains(ee.Stderr, []byte("-128")) ||
				bytes.Contains(bytes.ToLower(ee.Stderr), []byte("user canceled")) {
				return "", nil // a person, declining
			}
			return "", fmt.Errorf("%w: osascript failed rather than being dismissed: %s",
				ErrNoAnswer, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Reach reports whether a notification raised right now would actually be seen,
// and says why not when it would not.
//
// It exists because every layer reported success while nothing appeared. A
// coordinator request was posted, macOS accepted it, an active Focus swallowed
// the banner, and the operator asked why they had seen nothing. Dibs had no way
// to tell them, and no way to tell an agent that its request had not been shown
// to anybody: "delivered" and "seen" were the same word.
//
// Two things can silence it, and they need different sentences. Authorisation is
// per bundle AND per signature, so an ad-hoc rebuild silently revokes it: the
// remedy is a signing identity of Dibs' own. A Focus mode is the person's own
// choice and is nobody's fault: the remedy is to allow Dibs to break through, or
// to expect the ask in Notification Center rather than on screen.
func Reach() (ok bool, why string) {
	if !Available() {
		return false, "this platform has no notification route"
	}
	h := helper()
	if h == "" {
		return false, "Dibs.app is not installed beside the binary, so notifications " +
			"would be posted by osascript under Script Editor's name, without buttons"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// #nosec G204 -- h is resolved beside this binary; --status is a constant.
	out, err := exec.CommandContext(ctx, h, "--status").Output()
	switch strings.TrimSpace(string(out)) {
	case "authorized":
		// Allowed, which is not the same as visible.
		if f := focusOn(); f != "" {
			if bannersAreSilenced() {
				// Do not advise enabling Time Sensitive here. macOS reports it as
				// notSupported for this build, because it needs an entitlement Dibs
				// does not carry, so that advice cannot be followed and sends
				// somebody hunting a switch that is not there.
				return false, "a Focus mode is on (" + f + ") and this build cannot mark " +
					"a notification Time Sensitive, which is what would break through " +
					"one. Banners are silenced by construction, so a question or " +
					"request to you opens a WINDOW instead, which Focus does not " +
					"silence. Passive notices still wait in Notification Center"
			}
			return false, "a Focus mode is on (" + f + "), which silences banners. A " +
				"question or request asks to break through as Time Sensitive; allow " +
				"that for Dibs in System Settings > Notifications if it is not already"
		}
		return true, ""
	case "denied":
		return false, "notifications are turned off for Dibs in System Settings"
	case "not-determined":
		return false, "Dibs has never been granted notification permission. macOS ties " +
			"that grant to the app's SIGNATURE, so an ad-hoc rebuild revokes it: give " +
			"Dibs a signing identity of its own (see `task install`) or it will keep " +
			"asking and keep being silent in between"
	case "alerts-off":
		return false, "Dibs holds notification permission with every alert style off, " +
			"so nothing is ever shown"
	}
	if err != nil {
		return false, "the notifier could not be asked: " + err.Error()
	}
	return false, "the notifier gave no answer"
}

// focusOn returns the active Focus mode's identifier, or "".
//
// Read from the file macOS keeps rather than asked of an API, because there is
// no public one: NSUserNotificationCenter never exposed Focus, and Apple's
// supported answer for an app is to set an interruption level and accept the
// outcome. That is fine for DELIVERING and useless for DIAGNOSING, and this is
// the diagnosis path. Missing or unreadable means "no Focus", which is the
// honest reading: absence of evidence, and it only ever downgrades a warning.
func focusOn() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// #nosec G304 -- a fixed path under the user's own home directory.
	b, err := os.ReadFile(filepath.Join(home, "Library", "DoNotDisturb", "DB", "Assertions.json"))
	if err != nil {
		return ""
	}
	var doc struct {
		Data []struct {
			Records []struct {
				Details struct {
					Mode string `json:"assertionDetailsModeIdentifier"`
				} `json:"assertionDetails"`
			} `json:"storeAssertionRecords"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &doc) != nil || len(doc.Data) == 0 {
		return ""
	}
	for _, r := range doc.Data[0].Records {
		if m := r.Details.Mode; m != "" {
			return strings.TrimPrefix(m, "com.apple.focus.")
		}
	}
	return ""
}

// ErrCannotNotify says the machine refused to show anything, as distinct from a
// person who saw it and did not answer.
//
// The two were one value, and the cost was concrete: a request the operator
// could not see was indistinguishable from one they ignored, so nothing was
// reported and the asking agent waited out its deadline against a notification
// that had never appeared.
var ErrCannotNotify = errors.New("this machine will not show a notification")

// notAuthorised reports the helper's exit-2 contract: authorisation refused, or
// no bundle. Documented in internal/notify/app/notify_darwin.swift, where the
// exit codes are the API.
func notAuthorised(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 2
}

// bannersAreSilenced reports whether a notification would be delivered and not
// seen.
//
// Two conditions, and it takes both. A Focus mode suppresses banners; Time
// Sensitive is what breaks through one, and it needs an entitlement this build
// does not carry, so `timeSensitiveSetting` comes back notSupported. Either
// alone is survivable. Together they mean a banner is silenced by construction,
// which is a different thing from a person choosing not to answer.
//
// Deliberately conservative: anything it cannot determine reads as "not
// silenced", so the quiet path stays the default and an escalation happens only
// on positive evidence that the quiet path cannot work.
func bannersAreSilenced() bool {
	if focusOn() == "" {
		return false
	}
	h := helper()
	if h == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- h is resolved beside this binary; --settings is a constant.
	out, err := exec.CommandContext(ctx, h, "--settings").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "timeSensitive=0")
}

// askInAWindow puts the question on screen through the user's GUI session.
//
// The daemon is a LaunchAgent, and a process it forks directly has no route to
// the WindowServer: the helper starts, finds nothing to draw into, and exits at
// once. Measured three times, each looking like "the notification did not
// appear", and each time the same helper run from an interactive shell drew the
// window perfectly. That difference is the whole bug, and it is why the earlier
// osascript prompt never appeared either.
//
// `launchctl asuser <uid>` runs it inside the user's Aqua session, which is
// where a window can exist. The cost is that stdout does not come back, so the
// answer is left in a file this side creates and reads. The file holds a button
// label and nothing else: no token, no message body, nothing worth protecting
// beyond not leaving litter.
func askInAWindow(helperPath, title, body string, buttons []string) (string, error) {
	f, err := os.CreateTemp("", "dibs-answer-*")
	if err != nil {
		return "", err
	}
	answer := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(answer) }()

	ctx, cancel := context.WithTimeout(context.Background(), windowTimeout)
	defer cancel()
	argv := append([]string{
		"asuser", strconv.Itoa(os.Getuid()), helperPath, "--ask", "--out", answer,
		title, body,
	}, buttons...)
	// #nosec G204 -- launchctl is a fixed path, helperPath is resolved beside
	// this binary, and the rest is argv data the helper never interprets.
	if err := exec.CommandContext(ctx, "/bin/launchctl", argv...).Run(); err != nil {
		// A dismissed window exits non-zero, which is an answer.
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return "", err
		}
	}
	// Judged on CONTENT, not on whether the file could be read.
	//
	// This asked `os.ReadFile` for an error, and that error could never come:
	// CreateTemp above makes the file, so the read always succeeds and the
	// branch below was unreachable. A helper that crashed, was never installed,
	// or drew nothing at all left the empty file exactly as created, and this
	// returned ("", nil): no answer and no error, which every caller reads as a
	// deliberate "not now". So the failure this branch was written to report
	// was the one case it could not report. Found by a pre-release review.
	//
	// An empty answer is the honest signal either way. A dismissed window
	// writes nothing and exits non-zero; a helper that never drew writes
	// nothing and exits non-zero. Both are "nobody pressed anything", and the
	// caller needs to know that rather than infer patience.
	return answerFrom(answer)
}

// answerFrom reads the button the helper recorded, or reports that nobody
// pressed one.
//
// Split out because the shell-out above cannot be exercised in a test and this
// decision is the whole of the behaviour: there was no behavioural test of the
// window path at all, which is how the bug in the comment above survived.
func answerFrom(path string) (string, error) {
	out, err := os.ReadFile(path) // #nosec G304 -- a path the caller just created
	if err == nil {
		if pressed := strings.TrimSpace(string(out)); pressed != "" {
			return pressed, nil
		}
	}
	// "Dismissed" and "never drawn" are one answer to the caller and neither is
	// a decision. Reported as a failure, because a question nobody was shown is
	// not a question somebody declined: an agent otherwise waits out its
	// deadline while the operator sees nothing and nothing anywhere says which
	// of the two happened.
	return "", fmt.Errorf("%w: the window left no answer, so it was dismissed "+
		"without a press or never drawn at all", ErrNoAnswer)
}

// ErrNoAnswer means nothing came back from a question that was asked.
//
// Deliberately distinct from ErrCannotNotify, which means the machine will not
// notify at all. This one means we tried, and cannot tell whether a person
// declined or never saw it.
var ErrNoAnswer = errors.New("no answer")
