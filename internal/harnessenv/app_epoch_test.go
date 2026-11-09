// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"testing"
)

func TestChatGPTAppEpochSeparatesUnknownAbsentAndRunning(t *testing.T) {
	old := appProcessPass
	t.Cleanup(func() { appProcessPass = old })
	appProcessPass = func(context.Context) (*processPass, error) { return nil, errors.New("inventory failed") }
	if epoch, known := ChatGPTAppEpoch(); known || epoch != "" {
		t.Fatalf("failed probe looked known: %q %t", epoch, known)
	}
	appProcessPass = func(ctx context.Context) (*processPass, error) { return fixtureProcessPass(ctx, "", false), nil }
	if epoch, known := ChatGPTAppEpoch(); !known || epoch != "absent" {
		t.Fatalf("absent app looked unknown: %q %t", epoch, known)
	}
	appProcessPass = func(ctx context.Context) (*processPass, error) {
		return fixtureProcessPass(ctx, "123 Mon Oct 5 13:00:00 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n", false), nil
	}
	a, known := ChatGPTAppEpoch()
	if !known || len(a) != 64 {
		t.Fatalf("running epoch = %q %t", a, known)
	}
	appProcessPass = func(ctx context.Context) (*processPass, error) {
		return fixtureProcessPass(ctx, "456 Mon Oct 5 13:01:00 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n", false), nil
	}
	b, known := ChatGPTAppEpoch()
	if !known || a == b {
		t.Fatalf("replacement did not change epoch: %q %q %t", a, b, known)
	}
}
