// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

type appBindingDiagnostic struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
	Hint   string `json:"hint"`
}

func checkAppBindings(b *boardView, warn fixFn) {
	if b == nil {
		return
	}
	for _, alert := range b.AppBindingAlerts {
		warn("app navigation held for "+alert.Agent+": "+alert.Reason+" thread binding", alert.Hint)
	}
}

type boardQueueWake struct {
	ThreadState string `json:"thread_state"`
}

func goneQueueWake(a boardAgent) bool {
	return a.QueueWake != nil && a.QueueWake.ThreadState == "gone"
}

func goneQueueRepair(b *boardView) string {
	for _, a := range b.Agents {
		if a.Kind == "persistent" && wakeable(a) && goneQueueWake(a) {
			return "the app reports no rollout for this thread, so retries cannot reach it. " +
				"Bind the same agent from an existing app thread through its Dibs plugin; its mailbox is retained"
		}
	}
	return ""
}
