// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func init() {
	if os.Getenv("DIBS_TEST_BUNDLED_DAEMON_HELPER") == "1" {
		fmt.Print(helper())
		os.Exit(0)
	}
}

func TestBundledDaemonFindsItsActualNotifier(t *testing.T) {
	macos := filepath.Join(t.TempDir(), "Dibs.app", "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o700); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(macos, "dibd")
	notifier := filepath.Join(macos, "dibs-notify")
	for _, path := range []string{daemon, notifier} {
		if err := os.WriteFile(path, image, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	notifier, err = filepath.EvalSymlinks(notifier)
	if err != nil {
		t.Fatal("setup: canonical notifier path:", err)
	}
	t.Setenv("DIBS_TEST_BUNDLED_DAEMON_HELPER", "1")
	out, err := exec.Command(daemon).CombinedOutput()
	if err != nil || string(out) != notifier {
		t.Fatalf("daemon inside the real bundle layout lost its notifier: %v %q, want %q", err, out, notifier)
	}
}
