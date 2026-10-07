// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package peerwake

import (
	"context"
	"errors"
	"testing"
)

// A probe that times out is not a dead process. Measured: just after a daemon
// restart `ps` outlived its budget, a live Claude Code session was dropped
// from discovery, and its sender was told the agent was pull-only.
func TestASlowProbeDoesNotDeclareASessionDead(t *testing.T) {
	prev := psLstart
	t.Cleanup(func() { psLstart = prev })

	psLstart = func(ctx context.Context, _ int) ([]byte, error) {
		<-ctx.Done() // ps never answered inside the budget
		return nil, ctx.Err()
	}
	if !Alive(4242, "Wed Sep 30 14:58:31 2026") {
		t.Error("a timed-out probe declared the session dead")
	}

	psLstart = func(context.Context, int) ([]byte, error) {
		return nil, errors.New("exit status 1") // ps for a pid that does not exist
	}
	if Alive(4242, "") {
		t.Error("a pid ps says does not exist read as alive")
	}
}
