package notify

import (
	"os"
	"strings"
	"testing"
)

// Every string this package sends originates with an AGENT. Interpolating one
// into AppleScript source would hand an agent `do shell script`, so nothing is
// interpolated: the scripts are constants with an `on run argv` handler and the
// text arrives as arguments.
//
// This is the guard, and it is a source check rather than a behaviour check on
// purpose. The behaviour is verified once, below, against the real interpreter;
// what rots is somebody reaching for fmt.Sprintf in a hurry, and only reading
// the file catches that.
func TestNoScriptInThisPackageIsBuiltFromInput(t *testing.T) {
	src, err := os.ReadFile("notify.go")
	if err != nil {
		t.Fatal(err)
	}
	// Code only. The package comment says "never build a script with
	// fmt.Sprintf in this file", and a guard that cannot tell its own warning
	// from the thing it warns about fires on the prose forever.
	var code strings.Builder
	for _, line := range strings.Split(string(src), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	body := code.String()
	for _, forbidden := range []string{"fmt.Sprintf", "+ script", "script +", `"-e", "`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("notify.go contains %q: a script assembled from input is an agent with "+
				"`do shell script`, which is the one thing this package must never allow",
				forbidden)
		}
	}
	// And every script really is a handler, so its arguments are data.
	for _, s := range []string{banner, alert, prompt, pick} {
		if !strings.HasPrefix(s, "on run argv") {
			t.Errorf("a script does not take its input as argv:\n%s", s)
		}
	}
}

// The safety argument, against the real interpreter rather than a description
// of it. A body that would be code if interpolated must come back as text.
func TestAgentTextCannotBecomeCode(t *testing.T) {
	if !Available() {
		t.Skip("no notification route on this platform")
	}
	const echo = `on run argv
  return item 1 of argv
end run`
	hostile := `" & (do shell script "touch /tmp/dibs-notify-pwned") & "`
	got, err := run(echo, hostile)
	if err != nil {
		t.Fatal(err)
	}
	if got != hostile {
		t.Errorf("the interpreter did not return the text verbatim:\n got %q\nwant %q", got, hostile)
	}
	if _, err := os.Stat("/tmp/dibs-notify-pwned"); err == nil {
		_ = os.Remove("/tmp/dibs-notify-pwned")
		t.Fatal("agent text was EXECUTED: this package hands an agent arbitrary shell")
	}
}

// A dismissed prompt is an answer, not a fault: "the human said no" and "the
// machine could not ask" must not look the same to the caller.
func TestAlertsRefuseAnImpossibleNumberOfButtons(t *testing.T) {
	if _, err := Ask("t", "b"); err == nil {
		t.Error("an alert with no buttons was accepted")
	}
	if _, err := Ask("t", "b", "a", "b", "c", "d"); err == nil {
		t.Error("four buttons was accepted; macOS caps a dialog at three, and the extra " +
			"would be silently dropped")
	}
	if _, err := Pick("t", "b"); err == nil {
		t.Error("a picker with nothing to choose from was accepted")
	}
}

// A test run must not put a dialog on somebody's screen.
//
// underTest() catches a test BINARY and that is not enough: the end-to-end
// suites spawn a real dibd, which is a production binary by every measure and
// notifies accordingly. The operator watched fixture dialogs appear on every
// test run, reading "checker asks: Should I proceed with the rename?", with
// buttons that answered a daemon about to be killed.
//
// One variable, set on the test tasks, inherited by every daemon they spawn,
// because the suites pass their whole environment down. Five spawn sites each
// remembering is how this comes back.
func TestNotificationsCanBeSilencedForATestRun(t *testing.T) {
	t.Setenv("DIBS_NOTIFY", "off")
	if !silenced() {
		t.Error("DIBS_NOTIFY=off did not silence notifications, so a test run can " +
			"still put a dialog in front of whoever is at the keyboard")
	}
	if Available() {
		t.Error("Available() ignores the kill switch")
	}
	// Off for that value ONLY. A typo must not quietly disable the one path
	// that exists because the person is not in a loop to notice its absence,
	// which would be the same class of failure in the opposite direction.
	for _, v := range []string{"on", "", "0", "false", "OFF"} {
		t.Setenv("DIBS_NOTIFY", v)
		if silenced() {
			t.Errorf("DIBS_NOTIFY=%q silenced notifications; only \"off\" may", v)
		}
	}
}
