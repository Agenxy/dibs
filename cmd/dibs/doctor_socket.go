// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

type boardSocketWake struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Hint   string `json:"hint"`
}

func checkSocketBindings(b *boardView, warn fixFn) {
	if b == nil {
		return
	}
	for _, a := range b.Agents {
		if a.SocketWake != nil && a.SocketWake.State == "refused" {
			warn(a.ID+": socket wake refused: "+a.SocketWake.Reason, a.SocketWake.Hint)
		}
	}
}
