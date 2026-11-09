// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/procexec"
)

func TestSelfProcessStartDoesNotRunInventoryAndMatchesPS(t *testing.T) {
	probe := procexec.Command(t.Context(), "ps", "-o", "lstart=", "-p", fmt.Sprint(os.Getpid()))
	probe.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	raw, err := probe.Output()
	reference := strings.Join(strings.Fields(string(raw)), " ")
	if err != nil || reference == "" {
		t.Fatalf("setup: actual ps start stamp unavailable: %q %v", reference, err)
	}
	original := appProcessPass
	t.Cleanup(func() { appProcessPass = original })
	calls := 0
	appProcessPass = func(context.Context) (*processPass, error) {
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
