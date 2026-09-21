package notify

import (
	"os"
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

// A notification daemon that comes back is used again.
//
// The Linux probe answered once per process, so a notify-send that was
// missing for one measurement (a restarting notification daemon, a
// five-second deadline missed under load) switched this daemon's
// notifications off until somebody restarted it, while `dibs doctor` in a
// fresh process reported them healthy: a request for approval then waited
// on the board with nothing on screen and nothing saying so. A NO is
// measured again after a short while; a YES is kept, because the send
// itself reports a failure. Round thirty-four of the pre-release review.
func TestAFailedLinuxProbeIsMeasuredAgain(t *testing.T) {
	// The stand-in notify-send this package already uses, present or not
	// according to `present`: a host whose notification daemon comes back.
	stubNotifySend(t, "0.8.3")
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	present := false
	notifySend = func() string {
		if present {
			return self
		}
		return ""
	}
	resetProbe()

	if ok, _ := linuxProbeCached(); ok {
		t.Fatal("setup: nothing has probed yet, so there is no answer to cache")
	}
	if ok, _ := linuxProbe(); ok {
		t.Fatal("setup: the probe said yes with no notify-send")
	}
	// The notification daemon comes back. Within the retry window the old
	// answer stands, which is what keeps this off the writer loop.
	present = true
	if ok, _ := linuxProbe(); ok {
		t.Error("a failed probe was re-measured immediately: every send would re-probe")
	}
	// Past it, the answer is measured again.
	probeMu.Lock()
	probeAt = probeAt.Add(-2 * probeRetryAfter)
	probeMu.Unlock()
	if ok, why := linuxProbe(); !ok {
		t.Fatalf("the probe still says no (%q) after the notification daemon came back: this "+
			"daemon's notifications are off until somebody restarts it, while a fresh `dibs "+
			"doctor` reports them healthy", why)
	}
	// And a YES is kept: no re-probing a working host.
	present = false
	probeMu.Lock()
	probeAt = probeAt.Add(-2 * probeRetryAfter)
	probeMu.Unlock()
	if ok, _ := linuxProbe(); !ok {
		t.Error("a working host was re-probed and downgraded: the send itself reports a failure, " +
			"and re-measuring costs every caller two subprocesses")
	}
}
