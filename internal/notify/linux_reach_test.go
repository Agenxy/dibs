package notify

import (
	"strings"
	"testing"
)

// A Linux host with no notify-send is told what is missing, in the words an
// operator acts on: the package, the fact that nothing can ASK, and the board
// where the buttons are regardless. Issue #90 first made this sentence
// honest; #63 gave the platform a notifier, so the sentence now names what
// to install rather than what does not exist.
func TestReachOnLinuxSaysApprovalsWaitOnTheBoard(t *testing.T) {
	t.Setenv(silenceEnv, "")
	old, oldFind := goos, notifySend
	goos = "linux"
	notifySend = func() string { return "" } // a host without libnotify
	resetProbe()
	t.Cleanup(func() { goos, notifySend = old, oldFind; resetProbe() })

	ok, why := Reach()
	if ok {
		t.Fatal("Reach claims notifications reach a person on linux with no notify-send")
	}
	for _, want := range []string{"libnotify", "ASK", "dibs web"} {
		if !strings.Contains(why, want) {
			t.Errorf("the explanation does not mention %q, which is the part an "+
				"operator acts on:\n  %s", want, why)
		}
	}
}
