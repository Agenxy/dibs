// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/ui"
)

func (d *diagnosis) note(what string) {
	d.checks = append(d.checks, doctorCheck{Level: "note", What: what})
	d.prose(ui.Dim(what))
}

func checkNotificationRoute(ok, note reportFn, warn fixFn) {
	if status, err := notify.NotificationBrokerStatus(); err == nil && status.Held {
		note(fmt.Sprintf("Dibs notification broker owns this Mac's notification route (PID %v); "+
			"a second notifying parent refuses to post", status.PIDs))
	}
	// Posting capability is a check; the person's Focus is an informational
	// state. Neither can establish whether a banner was actually seen.
	if reaches, why, settings := notify.ReachWithSettings(); reaches {
		ok("Dibs can post native notifications with action buttons; posting does not confirm they were seen")
		if runtime.GOOS == "darwin" {
			if info := strings.TrimSpace(settings.Summary() + " " + settings.Information()); info != "" {
				note(info)
			}
			if settings.NeedsAttention() {
				warn("Dibs notification settings may hide or shorten approvals", settings.Hints(true))
			}
		} else if why != "" {
			note(why)
		}
	} else if why != "" {
		warn("agents cannot reach you by notification", why)
	}
}
