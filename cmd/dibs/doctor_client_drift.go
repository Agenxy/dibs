// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/agenxy/dibs/internal/clientdrift"
	"github.com/agenxy/dibs/internal/plugins"
)

func (d *diagnosis) checkLocalClientVersions() {
	home, _ := os.UserHomeDir()
	if older, shipped := plugins.OlderClaudeVersions(home); len(older) > 0 {
		d.warn(fmt.Sprintf("Claude Code's Dibs plugin includes older versions (%s); this build ships %s",
			strings.Join(older, ", "), shipped), plugins.ClaudeUpdate)
	}
	// The invoking shell and the service can have different PATHs. Compare
	// this shell to the daemon only when the board names this same host.
	reference := version
	if b, err := boardSnapshot(); err == nil && b.HostID == hostID() && b.DaemonBuild != "" {
		reference = b.DaemonBuild
	}
	for _, binary := range clientdrift.OlderBinaries(os.Getenv("PATH"), reference) {
		d.warn(binary.Description(), clientdrift.PathUpdate)
	}
}
