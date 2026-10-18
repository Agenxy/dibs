// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package liveness

import (
	"os"
	"os/exec"
	"testing"
)

func TestMergeProcessObservationUsesTheKernelAndRetainsAbsence(t *testing.T) {
	var p Poller
	if live, known := p.ObservePID(os.Getpid()); !known || !live {
		t.Fatal("own live process not observed")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMergeExitedProcessFixture$") // #nosec G204 -- this test binary
	if err := child.Run(); err != nil {
		t.Fatal("setup: child did not exit successfully", err)
	}
	if child.ProcessState == nil || !child.ProcessState.Exited() {
		t.Fatal("setup: no confirmed child exit")
	}
	if live, known := p.ObservePID(child.Process.Pid); !known || live {
		t.Fatal("confirmed exited process not absent")
	}
}

func TestMergeExitedProcessFixture(t *testing.T) {}
