package main

import (
	"runtime"

	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/ui"
)

func (d *diagnosis) note(what string) {
	d.checks = append(d.checks, doctorCheck{Level: "note", What: what})
	d.prose(ui.Dim(what))
}

func checkNotificationRoute(ok, note reportFn, warn fixFn) {
	// Posting capability is a check; the person's Focus is an informational
	// state. Neither can establish whether a banner was actually seen.
	if reaches, why, settings := notify.ReachWithSettings(); reaches {
		ok("Dibs can post native notifications with action buttons; posting does not confirm they were seen")
		if runtime.GOOS == "darwin" && settings.NeedsAttention() {
			if summary := settings.Summary(); summary != "" {
				note(summary)
			}
			warn("Dibs notification settings may hide or shorten approvals", settings.Hints(true))
		} else if why != "" {
			note(why)
		}
	} else if why != "" {
		warn("agents cannot reach you by notification", why)
	}
}
