package main

import (
	"os"
	"strings"
	"testing"
)

// A stop that timed out still arms the restart, so an upgrade cannot leave the
// board down.
//
// `doStop` sends SIGTERM and then waits for the process to go. Returning early
// when that wait expired treated a DELIVERED signal as though nothing had
// happened: upgrade printed "could not stop the daemon, so nothing else was
// changed", the daemon exited a few seconds later, launchd left it down because
// a clean exit is not a crash, and a 32-agent board vanished. Measured on the
// machine this was written on, by doing it.
//
// The property is narrow and worth pinning: whatever else fails, the code path
// that decides "am I responsible for restarting this" must treat a timed-out
// stop as a stop.
func TestATimedOutStopStillArmsTheRestart(t *testing.T) {
	src := readSource(t, "upgrade.go")

	// Bounded to the stop block, checked rather than assumed: an Index that
	// misses returns -1, and slicing on it would either panic or silently guard
	// the wrong region, which is worse than not guarding at all.
	from := strings.Index(src, `step("stopping the daemon")`)
	if from < 0 {
		t.Fatal("the stop step is no longer spelled this way; re-read upgrade.go " +
			"before assuming the property still holds")
	}
	stop := src[from:]
	to := strings.Index(stop, "restored := false")
	if to < 0 {
		t.Fatal("the recovery block this property depends on is gone; re-read " +
			"upgrade.go rather than trusting this test")
	}
	stop = stop[:to]

	set := strings.Index(stop, "stopped = true")
	fail := strings.Index(stop, "return fmt.Errorf")
	if set < 0 || fail < 0 {
		t.Fatal("the stop block no longer has the shape this guards; re-read it " +
			"before assuming the property still holds")
	}
	if set > fail {
		t.Error("upgrade returns on a failed stop BEFORE marking the daemon stopped, " +
			"so the recovery that restarts it never arms. A SIGTERM has already " +
			"been delivered at that point: the daemon goes away, nothing brings it " +
			"back, and the operator is told nothing changed")
	}
	// The old message, not the phrase. The block quotes it in a comment
	// explaining what went wrong, and a guard that cannot tell a fix from its own
	// explanation reports a defect that is not there.
	if strings.Contains(stop, `Errorf("could not stop the daemon, so nothing else was changed`) {
		t.Error("the failure still claims nothing was changed, after sending a signal " +
			"that stops the daemon")
	}
}

// And the stop error itself says the signal landed.
func TestTheStopFailureSaysTheSignalWasDelivered(t *testing.T) {
	src := readSource(t, "stop.go")
	i := strings.Index(src, "has been sent SIGTERM")
	if i < 0 {
		t.Fatal("the stop timeout no longer says a SIGTERM was delivered. An " +
			"operator reading it decides whether their board is still up, and the " +
			"honest answer is that it is on its way down")
	}
	tail := src[i:]
	for _, want := range []string{"STOPPING", "nothing will restart it", "dibd -dir"} {
		if !strings.Contains(tail[:min(len(tail), 700)], want) {
			t.Errorf("the message does not tell the operator %q, which is the part "+
				"they have to act on", want)
		}
	}
}

// readSource reads a file from this package, so a guard can assert about code
// whose behaviour needs a live daemon to exercise. Narrow on purpose: these two
// properties are about which branch runs and what it says, and the alternative
// is a test that has to stop a real daemon to find out.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
