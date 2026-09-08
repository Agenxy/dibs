package main

import "testing"

// The "nothing to do" check asked the daemon at DIBS_ADDR what it served,
// and the daemon the plan replaces is the registry's: with the target on an
// older build and another configured board on the new one, it concluded
// nothing to do and left the target alone. The plan asks the daemon it
// recorded.
func TestTheUpgradeAsksTheDaemonItWillReplace(t *testing.T) {
	t.Setenv("DIBS_ADDR", "http://127.0.0.1:4999")
	p := &plan{dir: t.TempDir(), running: daemonState{addr: "127.0.0.1:4777"}}
	if got := runningOrigin(p); got != "http://127.0.0.1:4777" {
		t.Fatalf("the upgrade asks %q, not the daemon it will replace at 127.0.0.1:4777", got)
	}
	p.running.addr = "https://10.0.0.9:4777"
	if got := runningOrigin(p); got != "https://10.0.0.9:4777" {
		t.Fatalf("a recorded scheme was not kept: %q", got)
	}
}
