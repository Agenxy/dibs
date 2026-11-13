// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package procinfo

import (
	"os"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestUnreapedZombieIsAbsentFromNativeIdentity(t *testing.T) {
	if started, err := Start(os.Getpid()); err != nil || started.IsZero() {
		t.Fatal("setup: live identity unavailable:", started, err)
	}
	pid := testexec.Zombie(t)
	if started, err := Start(pid); !Gone(err) || !started.IsZero() {
		t.Errorf("exited child still has a live native identity: start=%v err=%v", started, err)
	}
	rows, err := Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	live := false
	for _, row := range rows {
		if row.PID == pid {
			t.Error("unreaped zombie remained in the native process snapshot")
		}
		if row.PID == os.Getpid() {
			live = true
		}
	}
	if !live {
		t.Fatal("filter removed the live process as well")
	}
}
