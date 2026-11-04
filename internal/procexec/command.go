// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package procexec bounds output-pipe drain for context-bound child processes.
package procexec

import (
	"context"
	"os/exec"
	"time"
)

// Command is exec.CommandContext with a one-second WaitDelay. A deadline kills
// the child, but a grandchild can retain its output descriptors; without this
// bound Wait can outlive both the child and its context indefinitely.
// Callers keep ownership of their context, stdio and any explicit WaitDelay.
// WaitDelay does not bound a caller's direct StdoutPipe read before Wait;
// that reader needs its own context cancellation and close.
// ErrWaitDelay remains an error: only a caller knows whether incomplete output
// makes its result unknown or a successful launch is already sufficient.
func Command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	// #nosec G204 G702 -- callers own executable selection and typed argv; this wrapper adds no shell or input surface.
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.WaitDelay = time.Second
	return cmd
}
