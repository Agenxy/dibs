// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "fmt"

func checkMailboxCapacity(b *boardView, warn fixFn) {
	for _, a := range b.Agents {
		if c := a.MailboxCapacity; c != nil && c.NearCapacity {
			warn(fmt.Sprintf("%s mailbox capacity %d/%d (%d accepted obligations, %d announced FYIs retained separately)",
				a.ID, c.Depth, c.Limit, c.AcceptedDebt, c.AnnouncedFYIs), c.Hint)
		}
	}
}
