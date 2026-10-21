// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSelfProcessStartDoesNotRunInventoryAndMatchesPS(t *testing.T) {
	processes, err := processInventory()
	reference := processes[os.Getpid()].start
	if err != nil || reference == "" {
		t.Fatalf("setup: actual ps start stamp unavailable: %q %v", reference, err)
	}
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	calls := 0
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("self lookup must not launch an inventory subprocess")
	}
	t.Setenv("TZ", "America/Los_Angeles")
	got := ProcessStart(os.Getpid())
	if calls != 0 {
		t.Fatalf("own process start lookup launched %d inventory subprocesses", calls)
	}
	if got != reference {
		t.Fatalf("native own start=%q, want actual UTC ps stamp=%q", got, reference)
	}
}
