// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

func (m *Message) consumedRetentionExpired(now time.Time, legacyWindow time.Duration) bool {
	if !m.RetainUntil.IsZero() {
		// The recorded deadline is the complete decision for new responses.
		// Compare wall clocks on both sides, exactly as replay will; an old
		// done timestamp must not override a later correction's decision.
		return !now.UTC().Before(m.RetainUntil)
	}
	return now.Sub(m.TerminalAt) > legacyWindow // historical operations unchanged
}
