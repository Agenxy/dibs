// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "testing"

// The retired setting still parses, and the daemon says so.
//
// `[wake] remind_stale_after` governed a line telling a session it had not
// coordinated and should check_in. That line is gone: the daemon establishes
// liveness from its own evidence now, so the reminder could only ever reach
// agents whose liveness was already provable.
//
// The key is not deleted from the config struct, because an operator who set
// it should not have their daemon fail to start over a setting that stopped
// mattering. It is reported instead, since a setting that is read and silently
// does nothing is worse than one that is refused.
func TestTheRetiredStaleReminderIsReportedRatherThanIgnored(t *testing.T) {
	if retiredStaleReminder(WakeConfig{}) {
		t.Error("an operator who never set it is told their config carries a retired key")
	}
	for _, v := range []string{"2h", "off", "1m", "  30m  "} {
		if !retiredStaleReminder(WakeConfig{RemindStaleAfter: v}) {
			t.Errorf("remind_stale_after = %q was read and silently did nothing. An "+
				"operator who set it believes something is happening, and nothing is", v)
		}
	}
}
