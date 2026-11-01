// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/harnessenv"
)

const catalogRemedy = "Use the client's own tool refresh if you choose one, or start a new chat for that agent. " +
	"Dibs cannot safely reload just this existing Codex chat; no global reload or config change was made. " +
	"For lost-seat recovery, ask the coordinator to preview merge_agents(agent, into, dry_run:true); " +
	"a recovery proposal uses the survivor's existing respond(approve)."

func checkBridgeToolCatalogs(b *boardView, warn fixFn) {
	if b.HostID != hostID() {
		return
	} // process IDs are evidence only on this host
	processes := make([]harnessenv.BridgeIncarnation, 0, len(b.BridgeToolCatalogs))
	for _, row := range b.BridgeToolCatalogs {
		processes = append(processes, harnessenv.BridgeIncarnation{PID: row.PID, Start: row.StartedAt})
	}
	if len(processes) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, err := harnessenv.AppForBridges(ctx, processes)
	if err != nil {
		warn("connected-session tool catalog inventory is unknown",
			"A local process inventory failed; retry dibs doctor. No catalog freshness was confirmed.")
		return
	}
	for i, row := range b.BridgeToolCatalogs {
		if !observed[i].Present {
			continue
		} // exited/reused/unknown is not connected proof
		label := fmt.Sprintf("connected session bridge pid %d", row.PID)
		if len(row.Agents) > 0 {
			label += " (" + strings.Join(row.Agents, ", ") + ")"
		}
		switch {
		case row.Hash == "":
			warn(label+" has an unknown served tool catalog", catalogRemedy)
		case row.Hash != row.CurrentHash:
			warn(label+" was served a tool catalog different from this daemon", catalogRemedy)
		}
	}
}
