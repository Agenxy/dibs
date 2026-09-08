package main

import (
	"os"
	"strings"
	"testing"
)

// The ordering guard that used to live here compared the positions of two
// strings in upgrade.go and passed against the bug it was written for: the
// recovery defer was registered AFTER the stop, so a failed stop returned before
// it existed, and no string order could see that. The pre-release review ran
// it to prove the point. Its replacement, TestAFailedStopStillRestartsTheDaemon,
// runs the cutover with a stop that fails and asks whether start was called.

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
