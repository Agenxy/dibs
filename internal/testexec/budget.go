// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testexec

import (
	"context"
	"testing"
	"time"
)

// budgetMargin leaves the test binary time to report a failure and clean up
// before `go test -timeout` kills it with nothing but a goroutine dump.
const budgetMargin = 30 * time.Second

// BuildContext bounds a test that builds or runs native code by the test
// binary's own deadline, never by a fixed figure. The fixed figures (one,
// two and four minutes) held on an idle machine and killed builds midway on
// a laptop running the whole parallel suite, where a single bundle build that
// takes 17 seconds alone took more than four minutes. A failure then read as
// "signal: killed" and named no product fault at all. min is the floor used
// when the binary has no deadline (go test -timeout 0) or almost none left.
func BuildContext(t testing.TB, minimum time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	deadline, ok := t.(interface{ Deadline() (time.Time, bool) })
	if ok {
		if d, set := deadline.Deadline(); set {
			if until := time.Until(d) - budgetMargin; until > minimum {
				return context.WithTimeout(context.Background(), until)
			}
		}
	}
	return context.WithTimeout(context.Background(), minimum)
}
