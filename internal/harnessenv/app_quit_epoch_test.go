// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"testing"
)

// Process observation, not a manually supplied epoch: a main-process Quit
// leaves a readable bundled codex behind, and only a new main counts as return.
func TestChatGPTQuitIgnoresAReadableBundledCodexEpoch(t *testing.T) {
	previous := appProcessPass
	t.Cleanup(func() { appProcessPass = previous })
	main := "123 Mon Oct 5 13:00:00 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n"
	remnant := "456 Mon Oct 5 13:00:01 2026 /Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex\n"
	raw := main + remnant
	appProcessPass = func(ctx context.Context) (*processPass, error) { return fixtureProcessPass(ctx, raw, false), nil }
	first, known := ChatGPTAppEpoch()
	if !known || len(first) != 64 {
		t.Fatalf("setup: main did not establish an epoch: %q %v", first, known)
	}
	raw = remnant
	if epoch, known := ChatGPTAppEpoch(); !known || epoch != "absent" {
		t.Fatalf("Quit read as another app incarnation: %q known=%v", epoch, known)
	}
	raw = "789 Mon Oct 5 13:01:00 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n" + remnant
	if next, known := ChatGPTAppEpoch(); !known || len(next) != 64 || next == first {
		t.Fatalf("new main process did not establish its own return: %q known=%v", next, known)
	}
}
