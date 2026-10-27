// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package testexec prepares private command fixtures before timed assertions.
package testexec

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// PrepareExecutable runs a fixture's explicit side-effect-free mode once, with
// its intended environment and directory, before the test starts measuring a
// product deadline. macOS can assess a fresh executable on its first launch.
// Execution is bounded by 10 seconds and output-pipe drain by one more.
// Failure is a setup failure, and a product budget never changes. The one
// retry is a launch the kernel refused with ETXTBSY: on Linux a sibling test's
// fork inherits the write descriptor of a fixture being written until that
// child execs (golang/go#22315), and execve refuses a file open for writing.
// The fixture never started, so trying again repeats nothing. #493 made the
// CLI fixtures parallel, and a hosted Linux run failed this way.
// Callers must not pass a real installed command or a mode that posts or opens.
func PrepareExecutable(t testing.TB, command *exec.Cmd) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	for {
		// #nosec G204 -- private fixture and explicit argv supplied by the test
		probe := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
		probe.Dir, probe.Env = command.Dir, command.Env
		probe.WaitDelay = time.Second
		out, err := probe.CombinedOutput()
		if errors.Is(err, syscall.ETXTBSY) && ctx.Err() == nil {
			select {
			case <-ctx.Done(): // the next launch reports the expired bound
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			t.Fatalf("executable fixture setup: %s %v after %s (bound 10s + 1s drain): %v; output: %s",
				command.Path, command.Args[1:], time.Since(started), err, out)
		}
		return
	}
}
