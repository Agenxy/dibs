// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/agenxy/dibs/internal/core"
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
	order       uint64
}

// ObserveBridgeToolCatalog records transport-admitted metadata without contact.
func (e *Engine) ObserveBridgeToolCatalog(ctx context.Context, token string, observation BridgeToolCatalog) error {
	_, err := e.query(ctx, func() core.Result {
		if e.toolCatalogs == nil {
			e.toolCatalogs = map[string]BridgeToolCatalog{}
		}
		key := fmt.Sprintf("%d@%s", observation.PID, observation.StartedAt)
		old, exists := e.toolCatalogs[key]
		if !exists && len(e.toolCatalogs) >= 512 {
			var oldest string
			for k, v := range e.toolCatalogs {
				if oldest == "" || v.order < e.toolCatalogs[oldest].order {
					oldest = k
				}
			}
			delete(e.toolCatalogs, oldest)
		}
		if observation.Revision < old.Revision {
			observation.Hash, observation.Revision = old.Hash, old.Revision
		}
		observation.Agents = slices.Clone(old.Agents)
		// The local transport admits this metadata. A token contributes only
		// its own label; lookup must not count this machinery as agent contact.
		if a := e.state.AgentByToken(token); a != nil && len(observation.Agents) < 8 &&
			!slices.Contains(observation.Agents, a.ID) {
			observation.Agents = append(observation.Agents, a.ID)
		}
		e.toolCatalogOrder++
		observation.order = e.toolCatalogOrder
		e.toolCatalogs[key] = observation
		return nil
	})
	return err
}

func (e *Engine) decorateToolCatalogs(b core.Result) {
	if len(e.toolCatalogs) == 0 {
		return
	}
	rows := make([]BridgeToolCatalog, 0, len(e.toolCatalogs))
	for _, v := range e.toolCatalogs {
		v.Agents = slices.Clone(v.Agents)
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
