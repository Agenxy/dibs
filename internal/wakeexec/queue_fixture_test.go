// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import "testing"

// The caller supplies a fresh queue and board for each attempt. Only missing
// readiness may be returned; failed product assertions fail the test directly.
func retryUnreadyQueueFixture(t *testing.T, trial func() error) {
	t.Helper()
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		err := trial()
		if err == nil {
			return
		}
		if attempt == attempts {
			t.Fatalf("setup: fixture never became ready in %d attempts: %v", attempts, err)
		}
		t.Logf("fixture readiness missing on attempt %d: %v", attempt, err)
	}
}
