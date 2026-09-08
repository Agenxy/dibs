package main

import (
	"errors"
	"strings"
	"testing"
)

// After a stop that timed out, a replacement started while the old daemon
// still holds the directory lock exits on it; recovery confirms the board
// answers and starts again while the old process drains.
func TestRecoveryStartsAgainUntilTheBoardAnswers(t *testing.T) {
	starts, confirms := 0, 0
	p := &plan{
		dir:     t.TempDir(),
		running: daemonState{addr: "127.0.0.1:1"},
		stop:    func(string) error { return errors.New("did not exit within 60s") },
		start:   func(_, _, _ string, _ daemonState) error { starts++; return nil },
		confirm: func(string) error {
			confirms++
			if confirms == 1 {
				return errors.New("the board did not answer")
			}
			return nil
		},
	}
	if err := p.cutover(); err == nil {
		t.Fatal("a failed stop was reported as a clean cutover")
	}
	if starts != 2 || confirms != 2 {
		t.Errorf("starts=%d confirms=%d: after the first start the board did not answer, and "+
			"nothing started it again; the board stays down while the report says recovered",
			starts, confirms)
	}
}

// A start whose process exits at once is a failed start, not a done one.
func TestAStartThatExitsAtOnceIsReported(t *testing.T) {
	err := startDaemon("/usr/bin/false", t.TempDir(), "", daemonState{})
	if err == nil || !strings.Contains(err.Error(), "exited at once") {
		t.Fatalf("a process that exited immediately was reported as started: %v", err)
	}
}

// A start that fails at once, the shape of a stop that timed out with the
// old daemon still holding the lock, is an attempt: recovery starts again.
func TestRecoveryRetriesAStartThatFailedAtOnce(t *testing.T) {
	starts := 0
	p := &plan{
		dir:     t.TempDir(),
		running: daemonState{addr: "127.0.0.1:1"},
		stop:    func(string) error { return errors.New("did not exit within 60s") },
		start: func(_, _, _ string, _ daemonState) error {
			starts++
			if starts == 1 {
				return errors.New("dibd exited at once: exit status 1")
			}
			return nil
		},
		confirm: func(string) error { return nil },
	}
	if err := p.cutover(); err == nil {
		t.Fatal("a failed stop was reported as a clean cutover")
	}
	if starts != 2 {
		t.Errorf("starts=%d, want 2: the first start exited on the old daemon's lock and recovery "+
			"gave up before the loop that exists for exactly that", starts)
	}
}
