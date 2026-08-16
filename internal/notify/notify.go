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
	"encoding/json"
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
		// Cancelled, or sent empty. Both are "no answer", which is an answer.
		return "", true
	}
	return strings.TrimSpace(string(out)), true
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
			return false, "a Focus mode is on (" + f + "), which silences notifications. " +
				"A question or request asks to break through as Time Sensitive; allow " +
				"that for Dibs in System Settings > Notifications, or expect asks to " +
				"wait in Notification Center"
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
