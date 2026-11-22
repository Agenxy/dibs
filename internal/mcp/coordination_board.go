// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"maps"

	"github.com/agenxy/dibs/internal/core"
)

const (
	coordinationBoardMode  = "coordination"
	coordinationBoardBytes = 24 * 1024
	coordinationSlots      = 2
	coordinationSlotItems  = 4
)

// Use the board already captured in this checkpoint. The byte budget is on
// the board in the MCP payload, independently of the existing mailbox page.
func coordinationBoard(res core.Result) core.Result {
	snapshot, ok := boardSnapshotForOrientation(res)
	out := maps.Clone(res)
	if !ok {
		out["board"] = map[string]any{"detail": coordinationBoardMode, "error": "board projection unavailable"}
		return out
	}
	board := map[string]any{
		"serial": snapshot.Serial, "detail": coordinationBoardMode,
		"agents": []json.RawMessage{}, "agents_total": len(snapshot.Agents),
		"claims": []json.RawMessage{}, "claims_total": len(snapshot.Claims),
		"spaces": len(snapshot.Spaces),
	}
	envelope, _ := json.Marshal(board)
	remaining := coordinationBoardBytes - len(envelope)
	// Keep capacity for claim scopes even when a fleet fills the roster.
	claims, claimBytes := coordinationRows(snapshot.Claims, remaining/3, rosterClaim)
	agents, _ := coordinationRows(snapshot.Agents, remaining-claimBytes, coordinationAgent)
	board["agents"], board["claims"] = agents, claims
	out["board"] = board
	return out
}

// Budget the actual escaped representation. Charge a comma for every row,
// including the first, so the two arrays always fit their combined cap.
func coordinationRows(
	rows []map[string]any, budget int, project func(map[string]any) map[string]any,
) ([]json.RawMessage, int) {
	initialBudget := budget
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		raw, err := json.Marshal(project(row))
		if err != nil || len(raw)+1 > budget {
			break
		}
		budget -= len(raw) + 1
		out = append(out, raw)
	}
	return out, initialBudget - budget
}

func coordinationAgent(agent map[string]any) map[string]any {
	row := rosterRow(agent)
	if work, ok := agent["work"]; ok {
		row["work"] = work
	}
	slots := asMaps(agent["slots"])
	if len(slots) == 0 {
		return row
	}
	declarations := make([]map[string]any, 0, min(len(slots), coordinationSlots))
	for _, slot := range slots[:min(len(slots), coordinationSlots)] {
		item := pick(slot, []string{"id", "activity", "waiting", "unchanged_for_s"})
		if value, _ := slot["text"].(string); value != "" {
			item["text"] = oneLine(value, 180)
		}
		for _, field := range []string{"refs", "holds"} {
			items, _ := slot[field].([]any)
			if len(items) > 0 {
				item[field] = items[:min(len(items), coordinationSlotItems)]
			}
			if len(items) > coordinationSlotItems {
				item[field+"_total"] = len(items)
			}
		}
		declarations = append(declarations, item)
	}
	row["slots"], row["declarations"] = len(slots), declarations
	return row
}
