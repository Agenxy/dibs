// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestBackgroundOpenIncompleteReceiptIsUnknownAndNeverRetried(t *testing.T) {
	oldHelper, oldOutput, oldLegacy, oldOS := backgroundHelper, backgroundOpenFixture, backgroundLegacyFixture, goos
	t.Cleanup(func() {
		backgroundHelper, backgroundOpenFixture, backgroundLegacyFixture, goos = oldHelper, oldOutput, oldLegacy, oldOS
	})
	goos = "darwin"
	backgroundFallbackNotice.Lock()
	oldLogged := backgroundFallbackNotice.logged
	backgroundFallbackNotice.Unlock()
	t.Cleanup(func() {
		backgroundFallbackNotice.Lock()
		backgroundFallbackNotice.logged = oldLogged
		backgroundFallbackNotice.Unlock()
	})
	backgroundHelper = func() string { return "private-fixture-helper" }
	calls, legacy := 0, 0
	backgroundOpenFixture = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
		calls++
		if argv[0] == "--status" {
			return []byte(`{"background_open":1}`), nil
		}
		return []byte(`{"version":1,"opened":true}`), exec.ErrWaitDelay
	}
	backgroundLegacyFixture = func(context.Context, []string) error { legacy++; return nil }
	err := OpenChatGPTBackground("codex://threads/private-fixture")
	if !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(err.Error(), "incomplete output from dibs-notify") || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("incomplete open receipt became a tool failure or success: %v", err)
	}
	if calls != 2 || legacy != 0 {
		t.Fatalf("ambiguous action was retried: helper=%d legacy=%d", calls, legacy)
	}
}
