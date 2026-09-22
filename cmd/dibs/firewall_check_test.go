package main

import (
	"runtime"
	"strings"
	"testing"
)

// A single-machine board says nothing about the firewall.
//
// Loopback is not filtered by the Application Firewall, so the question does
// not arise, and putting a firewall sentence in front of every operator who has
// no firewall question is how a diagnosis becomes something people stop
// reading. The check below is the one that fires on a hub; this is the one that
// keeps it quiet everywhere else.
func TestTheFirewallCheckSaysNothingAboutALoopbackBoard(t *testing.T) {
	t.Setenv("DIBS_ADDR", "127.0.0.1:4777")
	var said []string
	checkIncomingFirewall(
		func(s string) { said = append(said, "ok: "+s) },
		func(what, fix string) { said = append(said, "bad: "+what+" / "+fix) },
	)
	if len(said) != 0 {
		t.Fatalf("a loopback board has no firewall question; got %v", said)
	}
}

// And on an address other machines route to, it reaches a verdict rather than
// staying silent: this is the check that would have saved the afternoon the
// first hub deployment cost, and a check that runs and says nothing is the
// thing that was already there.
//
// What the verdict IS depends on the machine running the test, which is why
// this asserts that one was reached and the wording of each is asserted in
// internal/appfirewall, where the decision is made from captured output.
func TestTheFirewallCheckReachesAVerdictOnAReachableBoard(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the Application Firewall is macOS only")
	}
	// A host this machine does not own is still off loopback, which is the only
	// property the check reads the address for.
	t.Setenv("DIBS_ADDR", "192.0.2.10:4777")
	var said []string
	checkIncomingFirewall(
		func(s string) { said = append(said, "ok: "+s) },
		func(what, fix string) { said = append(said, "bad: "+what+" / "+fix) },
	)
	// No dibd on PATH and none running is the one case with nothing to judge,
	// and it is a real one in a sandbox, so it is not a failure here.
	if len(said) == 0 {
		t.Skip("no dibd to ask about on this machine")
	}
	if len(said) != 1 {
		t.Fatalf("one verdict per board, got %v", said)
	}
	if !strings.Contains(said[0], "192.0.2.10:4777") {
		t.Errorf("the verdict names the address it is about, got %q", said[0])
	}
}
