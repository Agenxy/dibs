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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		// #nosec G204 -- h is resolved beside this binary; the rest is argv data.
		out, err := exec.CommandContext(ctx, h, append([]string{title, "", body}, buttons...)...).Output()
		if err != nil {
			return "", nil // dismissed or timed out: an answer, not a fault
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
	return run(pick, append([]string{title, body}, choices...)...)
}

// Prompt asks for free text and returns it, or "" if dismissed.
func Prompt(title, body string) (string, error) {
	return run(prompt, title, body)
}

// Available reports whether a person can be reached from here at all, so a
// caller can say "this build cannot notify you" once rather than failing
// silently on every message.
func Available() bool { return runtime.GOOS == "darwin" }

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
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
