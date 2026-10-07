// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"github.com/agenxy/dibs/internal/core"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

// Display only. Raw per-agent labels and the host IDs used for coordination
// remain untouched. Remote labels come from the newest coordinated member,
// not a timestamp of the label itself; the fold does not record that timestamp.
func (e *Engine) labelBoardHosts(b core.Result) {
	latest := map[string]*core.Agent{}
	// The board minted these rows, so their host is the board's own. The
	// person's physical location is unknown (especially on a hub). Use the
	// reserved nonce indexes, never a caller's chosen name or harness label.
	// Their empty HostID is deliberate: no process or harness wake route uses
	// it, and host comparisons keep the conservative unknown-ID behaviour.
	human, reporter := e.humanRowLocked(), e.dibsRowLocked()
	rows, _ := b["agents"].([]map[string]any)
	for _, row := range rows {
		a := e.state.Agents[row["id"].(string)]
		if a == nil {
			continue
		}
		if a.Agent == nil || a.Agent.HostID == "" || a.Agent.Host == "" {
			continue
		}
		id := e.canonicalHost(a.Agent.HostID)
		if newerHostLabel(a, latest[id]) {
			latest[id] = a
		}
	}
	for _, row := range rows {
		id := row["id"].(string)
		if id == human || id == reporter {
			row["host"] = hostdisplay.Name()
			continue
		}
		a := e.state.Agents[id]
		if a == nil || a.Agent == nil {
			continue
		}
		label := e.boardHostLabel(a.Agent, latest)
		if label != "" {
			row["host"] = label
		}
	}
}

func newerHostLabel(a, old *core.Agent) bool {
	if old == nil || a.LastCoordination.After(old.LastCoordination) {
		return true
	}
	if !a.LastCoordination.Equal(old.LastCoordination) {
		return false
	}
	return a.CreatedSerial > old.CreatedSerial || (a.CreatedSerial == old.CreatedSerial && a.ID < old.ID)
}

func (e *Engine) boardHostLabel(info *core.AgentInfo, latest map[string]*core.Agent) string {
	id := e.canonicalHost(info.HostID)
	if id == "" {
		return info.Host
	}
	if id == e.HostID() || id == e.state.NodeID || e.hostAliases[id] {
		return hostdisplay.Name()
	}
	if newest := latest[id]; newest != nil {
		return newest.Agent.Host
	}
	return info.Host
}
