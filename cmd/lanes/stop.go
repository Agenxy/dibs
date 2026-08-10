package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/agenxy/lanes/internal/paths"
)

// selectDaemon picks the one daemon serving dir, and reports how many others
// were deliberately left alone.
//
// Split out from stopDaemon because this is the part that decides which process
// receives a signal, and a mistake here kills somebody else's fleet. It is a
// pure function of the registry so it can be tested without spawning daemons.
func selectDaemon(live []paths.Daemon, dir string) (mine *paths.Daemon, others int, err error) {
	var found []paths.Daemon
	for _, d := range live {
		// An entry we could not decode is always a stranger — IsStranger says so
		// — but check Unknown explicitly too: signalling a pid we could not
		// attribute is exactly the mistake this function exists to prevent.
		if d.Unknown || d.IsStranger(dir) {
			others++
			continue
		}
		found = append(found, d)
	}
	switch len(found) {
	case 0:
		return nil, others, nil
	case 1:
		return &found[0], others, nil
	default:
		// The flock makes this impossible; say so rather than picking one.
		return nil, others, fmt.Errorf("%d daemons claim %s, which the directory lock should "+
			"prevent — stop them by pid and report this", len(found), dir)
	}
}

// stopDaemon stops the daemon serving THIS data directory, and only that one.
//
// The documentation used to say `pkill lanesd`, which is wrong in a product
// whose whole design permits several deliberately isolated daemons on one
// machine: a broad kill by name takes down a colleague's fleet, or a scratch
// daemon somebody is mid-debug on, and reports success either way. Running
// agents lose their coordination and nothing tells them why.
//
// The registry already knows which process holds which directory — it is how a
// second daemon is refused — so a precise stop needs no new bookkeeping.
func stopDaemon(dir string) error {
	live, err := paths.LiveDaemons()
	if err != nil {
		return fmt.Errorf("reading the daemon registry: %w", err)
	}

	d, others, err := selectDaemon(live, dir)
	if err != nil {
		return err
	}
	if d == nil {
		// Not an error. "Stop what is not running" is satisfied.
		fmt.Printf("no daemon is running on %s\n", dir)
		if others > 0 {
			fmt.Printf("  %d other daemon(s) are running on other directories and were left alone\n", others)
		}
		return nil
	}
	proc, err := os.FindProcess(d.PID)
	if err != nil {
		return fmt.Errorf("pid %d from the registry: %w", d.PID, err)
	}
	// SIGTERM, not SIGKILL. The daemon closes its ledger and releases its claims
	// on the way out; killing it outright leaves a torn tail for the next start
	// to discard and every claim standing until it times out.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			fmt.Printf("daemon on %s had already exited\n", dir)
			return nil
		}
		return fmt.Errorf("signalling pid %d: %w", d.PID, err)
	}

	// Wait, so the caller can start a replacement immediately. Without this,
	// `lanes stop && lanesd &` races the outgoing process for the flock and the
	// new daemon refuses to start — which reads as a broken command.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Signal 0 tests for existence. An error here means the process is GONE,
		// which is the success we are waiting for — hence returning nil on a
		// non-nil error, which reads backwards and is why it is spelled out.
		//nolint:nilerr // an error from signal 0 means the process is GONE, which
		// is the success this loop waits for. nilerr sees `err != nil` followed by
		// `return nil` and cannot know the error IS the answer.
		if gone := proc.Signal(syscall.Signal(0)) != nil; gone {
			fmt.Printf("stopped lanesd (pid %d) on %s\n", d.PID, dir)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("lanesd (pid %d) did not exit within 10s of SIGTERM; "+
		"it is still holding %s. Send SIGKILL yourself if you are sure: kill -9 %d",
		d.PID, dir, d.PID)
}
