package main

import (
	"errors"
	"testing"
)

// F3: a stop that fails still restarts the daemon.
//
// The recovery defer was registered AFTER the stop it covers, so the stop's
// own failure path returned before the defer existed: a SIGTERM that landed
// but outran the wait left the daemon exiting with nothing armed to restart
// it, while the error promised a restart. The previous guard compared the
// order of two strings in the source and passed against exactly this. This one
// runs the cutover with a stop that fails and asks whether start was called.
// Found by the pre-release review, which ran the old test to prove the point.
func TestAFailedStopStillRestartsTheDaemon(t *testing.T) {
	started := false
	p := &plan{
		dir:     t.TempDir(),
		running: daemonState{addr: "127.0.0.1:1"},
		stop:    func(string) error { return errors.New("did not exit within 60s") },
		start:   func(_, _, _ string, _ daemonState) error { started = true; return nil },
		confirm: func(string) error { return nil },
	}
	if err := p.cutover(); err == nil {
		t.Fatal("a failed stop was reported as a clean cutover")
	}
	if !started {
		t.Error("the stop failed, the error promised a restart, and no start was attempted: " +
			"a SIGTERM that landed leaves the board down with nothing to bring it back")
	}
}
