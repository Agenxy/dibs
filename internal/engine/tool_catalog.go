// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/clientdrift"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/release"
)

// BridgeToolCatalog is an observation of bytes served, not client adoption,
// session authority, or model activity. Doctor checks the process incarnation
// before describing it as connected. This rebuildable inventory is bounded.
type BridgeToolCatalog struct {
	PID         int      `json:"pid"`
	StartedAt   string   `json:"started_at"`
	Hash        string   `json:"hash,omitempty"`
	Revision    uint64   `json:"revision,omitempty"`
	CurrentHash string   `json:"current_hash"`
	Agents      []string `json:"agents,omitempty"`
	Build       string   `json:"build"`
	Behind      bool     `json:"behind,omitempty"`
	order       uint64
	present     bool
}

// ObserveBridgeToolCatalog records transport-admitted metadata without contact.
func (e *Engine) ObserveBridgeToolCatalog(ctx context.Context, token string, observation BridgeToolCatalog) error {
	_, err := e.query(ctx, func() core.Result {
		if e.toolCatalogs == nil {
			e.toolCatalogs = map[string]BridgeToolCatalog{}
		}
		key := fmt.Sprintf("%d@%s", observation.PID, observation.StartedAt)
		old, exists := e.toolCatalogs[key]
		if !exists {
			e.limitBridgeCatalogs()
		}
		if observation.Revision < old.Revision {
			observation.Hash, observation.Revision = old.Hash, old.Revision
		}
		observation.Build = bridgeBuild(observation.Build)
		if observation.Build == "" {
			observation.Build = old.Build
		}
		observation.Behind = old.Behind
		if observation.Build != old.Build {
			order, err := release.Compare(observation.Build, build.Version)
			observation.Behind = err == nil && order < 0
		}
		observation.present = old.present || e.bridgeBuildWarnings[bridgeProcess(observation)]
		observation.Agents = slices.Clone(old.Agents)
		// The local transport admits this metadata. A token contributes only
		// its own label; lookup must not count this machinery as agent contact.
		if a := e.state.AgentByToken(token); a != nil && len(observation.Agents) < 8 &&
			!slices.Contains(observation.Agents, a.ID) {
			observation.Agents = append(observation.Agents, a.ID)
		}
		e.observeBridgeDrift(&observation)
		e.toolCatalogOrder++
		observation.order = e.toolCatalogOrder
		e.toolCatalogs[key] = observation
		return nil
	})
	return err
}

func (e *Engine) limitBridgeCatalogs() {
	if len(e.toolCatalogs) < 512 {
		return
	}
	var oldest string
	for k, v := range e.toolCatalogs {
		if oldest == "" || v.order < e.toolCatalogs[oldest].order {
			oldest = k
		}
	}
	delete(e.toolCatalogs, oldest)
}

func (e *Engine) decorateToolCatalogs(b core.Result) {
	if len(e.toolCatalogs) == 0 {
		return
	}
	rows := make([]BridgeToolCatalog, 0, len(e.toolCatalogs))
	for _, v := range e.toolCatalogs {
		v.Agents = slices.Clone(v.Agents)
		if v.Build == "" {
			v.Build = "unknown"
		}
		rows = append(rows, v)
	}
	slices.SortFunc(rows, func(a, b BridgeToolCatalog) int {
		if a.PID < b.PID {
			return -1
		}
		if a.PID > b.PID {
			return 1
		}
		return 0
	})
	b["bridge_tool_catalogs"] = rows
}

func (e *Engine) decorateBridgeBuildDrift(b core.Result) {
	if len(e.bridgeBuildWarnings) == 0 {
		return
	}
	count := 0
	names := make(map[string]bool)
	for _, row := range e.toolCatalogs {
		if !row.Behind || !row.present {
			continue
		}
		count++
		for _, id := range row.Agents {
			if e.state.Agents[id] != nil {
				names[e.agentName(id)] = true
			}
		}
	}
	if count > 0 {
		agents := make([]string, 0, len(names))
		for name := range names {
			agents = append(agents, name)
		}
		slices.Sort(agents)
		b["bridge_build_drift"] = core.Result{"count": count, "agents": agents}
	}
}

// These labels are display evidence and never authorize a bridge or agent.
func bridgeBuild(value string) string {
	const versionChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz.+-"
	if len(value) > 128 || strings.IndexFunc(value, func(r rune) bool {
		return !strings.ContainsRune(versionChars, r)
	}) >= 0 {
		return ""
	}
	return value
}

func bridgeProcess(row BridgeToolCatalog) harnessenv.BridgeIncarnation {
	return harnessenv.BridgeIncarnation{PID: row.PID, Start: row.StartedAt}
}

func (e *Engine) observeBridgeDrift(row *BridgeToolCatalog) {
	process := bridgeProcess(*row)
	if row.present && row.Behind && !e.bridgeBuildWarnings[process] {
		if e.bridgeBuildWarnings == nil {
			e.bridgeBuildWarnings = make(map[harnessenv.BridgeIncarnation]bool)
		}
		e.bridgeBuildWarnings[process] = true
		slog.Warn("connected session is running an older Dibs bridge",
			"pid", row.PID, "started_at", row.StartedAt, "build", row.Build, "daemon_build", build.Version,
			"fix", clientdrift.BridgeUpdate)
	}
}

// VerifyBridgeIncarnation updates the inventory after an independent local
// process probe. Claims that name an absent process do not count as connected.
func (e *Engine) VerifyBridgeIncarnation(ctx context.Context, pid int, started string, present bool) {
	_, _ = e.query(ctx, func() core.Result {
		key := fmt.Sprintf("%d@%s", pid, started)
		if !present {
			delete(e.bridgeBuildWarnings, harnessenv.BridgeIncarnation{PID: pid, Start: started})
		}
		if row, ok := e.toolCatalogs[key]; ok {
			row.present = present
			e.observeBridgeDrift(&row)
			e.toolCatalogs[key] = row
		}
		return nil
	})
}

// PruneBridgeBuilds refreshes the process evidence behind the drift row. Take
// one snapshot off the writer and drop only confirmed absent incarnations.
// Keep their catalog rows for diagnosis within the existing inventory bound.
func (e *Engine) PruneBridgeBuilds(ctx context.Context) {
	var bridges []harnessenv.BridgeIncarnation
	_, err := e.query(ctx, func() core.Result {
		for bridge := range e.bridgeBuildWarnings {
			bridges = append(bridges, bridge)
		}
		return nil
	})
	if err != nil || len(bridges) == 0 {
		return
	}
	observed, err := harnessenv.AppForBridges(ctx, bridges)
	if err != nil {
		return
	}
	for i, row := range observed {
		if !row.Present {
			e.VerifyBridgeIncarnation(ctx, bridges[i].PID, bridges[i].Start, false)
		}
	}
}
