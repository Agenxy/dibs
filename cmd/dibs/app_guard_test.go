// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAppGuardRejectsUnfakedBridge(t *testing.T) {
	if os.Getenv("DIBS_TEST_UNFAKED_BRIDGE") != "" {
		b := newWakeBridge("http://127.0.0.1", "fixture", "fixture-host", nil)
		_ = b.show.Open([]string{"/nonexistent/dibs-test-app-guard"})
		t.Fatal("unfaked app guard returned")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppGuardRejectsUnfakedBridge$") // this test binary only
	cmd.Env = append(os.Environ(), "DIBS_TEST_UNFAKED_BRIDGE=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unfaked app access in dibs CLI test") {
		t.Fatalf("bridge guard did not reject accidental app access: %v %s", err, out)
	}
}
