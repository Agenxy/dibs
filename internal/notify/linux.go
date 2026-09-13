package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The Linux notifier: notify-send from libnotify, with actions (issue #63).
//
// Being asked and answering is how a person stays the authority over a
// fleet, and on Linux that mechanism was absent: Available() was
// `goos == "darwin"`, so a request that needed the operator waited on the
// board until somebody looked. libnotify 0.7.10 (2022) gave notify-send
// exactly the shape Ask needs: `-A key=Label` declares a button, `--wait`
// blocks until the notification is acted on or dismissed, and the chosen
// key is printed on stdout. One subprocess, argv only, no shell: the same
// rule the macOS helper follows, and the same reason.
//
// Three honest limits, each its own Reach() sentence rather than a silent
// downgrade:
//
//   - no notify-send on PATH: nothing on this machine can ask;
//   - no session bus (headless): notify-send cannot reach a daemon, and
//     approvals wait on the board, which `dibs web` opens;
//   - libnotify older than 0.7.10: it can SHOW and cannot ask, and a banner
//     nobody can answer is not an approval path, so it is not offered as one.
//
// Text entry (Prompt) has no notify-send form and says so.

// notifySend is exec.LookPath("notify-send"), a variable so tests can point
// it at a stub that speaks the same argv.
var notifySend = func() string {
	p, err := exec.LookPath("notify-send")
	if err != nil {
		return ""
	}
	return p
}

// minNotifySend is the libnotify release that added --wait and --action.
var minNotifySend = [3]int{0, 7, 10}

// linuxUsable reports whether Ask can work here, and if not, why not.
func linuxUsable() (ok bool, why string) {
	bin := notifySend()
	if bin == "" {
		return false, "notify-send is not on PATH, so nothing on this machine can ASK " +
			"you anything: install libnotify (the package is usually libnotify-bin or " +
			"libnotify). Until then a request that needs your approval waits on the " +
			"board; open it with `dibs web`, the buttons are there"
	}
	if !sessionBus() {
		return false, "there is no session bus (no DBUS_SESSION_BUS_ADDRESS and no " +
			"$XDG_RUNTIME_DIR/bus), which is what a headless host looks like: notify-send " +
			"has no notification daemon to reach. A request that needs your approval " +
			"waits on the board; open it with `dibs web`, the buttons are there"
	}
	v, err := notifySendVersion(bin)
	if err != nil {
		return false, "notify-send would not report its version (" + err.Error() + "), " +
			"so whether it can carry buttons is unknown; approvals wait on the board (`dibs web`)"
	}
	if !atLeast(v, minNotifySend) {
		return false, "notify-send " + versionString(v) + " can show a notification and " +
			"cannot put buttons on one (that needs libnotify 0.7.10 or later), and a " +
			"question nobody can answer is not an approval path. Upgrade libnotify; " +
			"until then approvals wait on the board (`dibs web`)"
	}
	// The bus address and the client are necessary and not sufficient: the
	// DAEMON on the bus is what shows buttons, and some (a minimal or a
	// broken one) advertise no "actions" capability. Asked, not assumed.
	if ok, why := daemonHasActions(); !ok {
		return false, why
	}
	return true, ""
}

// dbusProbe is the path of a tool that can call GetCapabilities on the
// session bus: dbus-send (package dbus, or dbus-bin) first, gdbus (glib)
// second. A variable so tests can point it at a stub.
var dbusProbe = func() (bin string, gdbus bool) {
	if p, err := exec.LookPath("dbus-send"); err == nil {
		return p, false
	}
	if p, err := exec.LookPath("gdbus"); err == nil {
		return p, true
	}
	return "", false
}

// daemonHasActions asks the notification daemon whether it can show
// buttons, through org.freedesktop.Notifications.GetCapabilities. Without a
// tool to ask with, the answer is "unverified", said as such, rather than a
// yes nobody measured.
func daemonHasActions() (ok bool, why string) {
	bin, gdbus := dbusProbe()
	if bin == "" {
		return false, "neither dbus-send nor gdbus is on PATH, so whether the notification " +
			"daemon can show buttons cannot be verified (install dbus-bin, or libglib2.0-bin " +
			"for gdbus); until it is, approvals wait on the board (`dibs web`)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{
		"--session", "--print-reply", "--dest=org.freedesktop.Notifications",
		"/org/freedesktop/Notifications", "org.freedesktop.Notifications.GetCapabilities",
	}
	if gdbus {
		args = []string{
			"call", "--session", "--dest", "org.freedesktop.Notifications",
			"--object-path", "/org/freedesktop/Notifications",
			"--method", "org.freedesktop.Notifications.GetCapabilities",
		}
	}
	// #nosec G204 -- argv, no shell; bin is what PATH resolved.
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return false, "no notification daemon answered on the session bus (" + err.Error() + "): " +
			"nothing here can show a notification, and approvals wait on the board (`dibs web`)"
	}
	if !strings.Contains(string(out), "actions") {
		return false, "the notification daemon on this session bus advertises no \"actions\" " +
			"capability, so it can show a notification and cannot put buttons on one, and a " +
			"question nobody can answer is not an approval path; approvals wait on the board (`dibs web`)"
	}
	return true, ""
}

func sessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		// #nosec G703 -- a well-known socket under the runtime dir; only its
		// existence is read.
		if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
			return true
		}
	}
	return false
}

// notifySendVersion parses `notify-send --version`, which prints
// "notify-send 0.8.3".
func notifySendVersion(bin string) ([3]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- argv, no shell; bin is what PATH resolved.
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return [3]int{}, err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return [3]int{}, errors.New("empty --version output")
	}
	return parseVersion(f[len(f)-1])
}

func parseVersion(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) < 2 {
		return v, fmt.Errorf("%q is not a version", s)
	}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(strings.TrimRight(parts[i], "abcdefghijklmnopqrstuvwxyz-"))
		if err != nil {
			return v, fmt.Errorf("%q is not a version", s)
		}
		v[i] = n
	}
	return v, nil
}

func atLeast(v, want [3]int) bool {
	for i := range v {
		if v[i] != want[i] {
			return v[i] > want[i]
		}
	}
	return true
}

func versionString(v [3]int) string {
	return strconv.Itoa(v[0]) + "." + strconv.Itoa(v[1]) + "." + strconv.Itoa(v[2])
}

// linuxAsk shows the notification with one button per label and returns the
// label pressed, "" when dismissed.
//
// Buttons are keyed by index and labelled by the caller's text, so the text
// is data on the wire and never parsed on the way back: the answer is the
// index, mapped here.
func linuxAsk(title, body string, buttons []string) (string, error) {
	if silenced() {
		return "", ErrUnsupported // the process's off switch holds on every platform
	}
	bin := notifySend()
	if bin == "" {
		return "", ErrCannotNotify
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
	defer cancel()
	args := []string{"--app-name=Dibs", "--wait", "--urgency=normal"}
	for i, b := range buttons {
		args = append(args, "--action="+strconv.Itoa(i)+"="+b)
	}
	args = append(args, "--", title, body)
	// #nosec G204 -- argv only, no shell: title and body are arguments after
	// `--`, exactly as the macOS helper takes them.
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("%w: notify-send failed rather than being dismissed: %s",
				ErrNoAnswer, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", nil // dismissed, or the notification timed out: a person, declining
	}
	i, err := strconv.Atoi(key)
	if err != nil || i < 0 || i >= len(buttons) {
		return "", fmt.Errorf("%w: notify-send answered %q, which names no button", ErrNoAnswer, key)
	}
	return buttons[i], nil
}

// linuxBanner posts a passive notification: nothing to answer, nothing to wait for.
func linuxBanner(title, subtitle, body string) error {
	if silenced() {
		return ErrUnsupported
	}
	bin := notifySend()
	if bin == "" {
		return ErrCannotNotify
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	text := body
	if subtitle != "" {
		text = subtitle + "\n" + body
	}
	// #nosec G204 -- argv, no shell.
	return exec.CommandContext(ctx, bin, "--app-name=Dibs", "--", title, text).Run()
}
