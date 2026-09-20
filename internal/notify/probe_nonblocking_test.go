package notify

import (
	"testing"
	"time"
)

// Available never waits on the Linux probe; Reach does.
//
// Available is asked on the engine's single-writer loop when an agent writes
// to the human. The probe runs two subprocesses with five-second deadlines,
// and caching its answer removed the repeated stall and not the first one:
// startup warms the cache asynchronously, and a message to the human arriving
// before the probe had finished blocked every op and sweep behind it for up
// to ten seconds. Available now reads what is known and assumes reachable
// while the measurement is in flight (the send goes off the loop and settles
// against the real answer there); Reach, off the loop, still measures. Round
// seven of the pre-release review.
func TestAvailableDoesNotWaitOnTheLinuxProbe(t *testing.T) {
	t.Setenv(silenceEnv, "")
	old, oldProbe := goos, probeHost
	goos = "linux"
	started, release := make(chan struct{}), make(chan struct{})
	probeHost = func() (bool, string) {
		close(started)
		<-release // the measurement takes as long as the test says
		return false, "measured: no notifier"
	}
	resetProbe()
	t.Cleanup(func() { resetProbe(); goos, probeHost = old, oldProbe })

	begin := time.Now()
	ok := Available()
	if took := time.Since(begin); took > 200*time.Millisecond {
		t.Fatalf("Available took %v with the probe in flight: the writer loop waited on subprocesses", took)
	}
	if !ok {
		t.Error("Available answered no while the answer was still being measured; the off-loop send is what should decide")
	}
	<-started
	close(release)
	// Reach is the measurement, and waits for it.
	if ok, why := Reach(); ok || why != "measured: no notifier" {
		t.Errorf("Reach = %v %q, want the measured answer", ok, why)
	}
	// And once measured, Available reads it.
	if Available() {
		t.Error("Available still says yes after the probe measured no")
	}
}
