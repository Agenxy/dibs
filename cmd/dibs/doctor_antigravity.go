// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

func reportAntigravityCoverage(b *boardView, ok reportFn, warn fixFn) {
	if b == nil {
		return
	}
	for _, agent := range b.Agents {
		if agent.AntigravityWakeBound == nil || !wakeable(agent) {
			continue
		}
		if *agent.AntigravityWakeBound {
			ok("Antigravity desktop conversation recorded for " + agent.ID)
			continue
		}
		warn("Antigravity has supplied no verified conversation for "+agent.ID,
			"check in through Dibs inside the Antigravity desktop conversation")
	}
}

func countsForConfiguredWake(a boardAgent) bool {
	return a.Kind == "persistent" && a.AntigravityWakeBound == nil && wakeable(a)
}

func disabledWakeRoutes(b *boardView) string {
	if b != nil {
		for _, a := range b.Agents {
			if a.AntigravityWakeBound != nil && *a.AntigravityWakeBound {
				return "socket and configured command wakes are disabled"
			}
		}
	}
	return "no wake route at all: [wake] sockets = false and no [wake.exec] command"
}
