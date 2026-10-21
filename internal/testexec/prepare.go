// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package testexec prepares private command fixtures before timed assertions.
package testexec

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// PrepareExecutable runs a fixture's explicit side-effect-free mode once, with
// its intended environment and directory, before the test starts measuring a
// product deadline. macOS can assess a fresh executable on its first launch.
// Execution is bounded by 10 seconds and output-pipe drain by one more.
// Failure is a setup failure; this never retries or changes a product budget.
// Callers must not pass a real installed command or a mode that posts or opens.
func PrepareExecutable(t testing.TB, command *exec.Cmd) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, command.Path, command.Args[1:]...) // #nosec G204 -- private fixture and explicit argv supplied by the test
	probe.Dir, probe.Env = command.Dir, command.Env
	probe.WaitDelay = time.Second
	started := time.Now()
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("executable fixture setup: %s %v after %s (bound 10s + 1s drain): %v; output: %s", command.Path, command.Args[1:], time.Since(started), err, out)
	}
}
