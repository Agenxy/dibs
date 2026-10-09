// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// workStateOf is the row's `work`: idle, working, declared, waiting or stalled. Derived
// from what the agent declared and what the board has seen it do, never from
// whether its process is alive: a Codex agent in the ChatGPT app has no
// process between calls, and its row said "dormant (process gone)" while it
// was busy. Reported by k7-dev (Dibs #1152).
func (e *Engine) workStateOf(l *core.Agent) string {
	slots := e.workSlotsOf(l, time.Now())
	if len(slots) == 0 {
		return "idle"
	}
	if len(openOf(slots)) > 0 {
		// WORKING NEEDS EVIDENCE, not just a declaration. The first live board
		// read "working" beside "seen 6d ago" for rows whose declaration nobody
		// had touched in days: true of what they said, false of what they were
		// doing. Past workingWithin of silence the row says what the board
		// actually knows, that the work is declared.
		if time.Now().Round(0).Sub(e.lastEvidenceOf(l).Round(0)) > workingWithin {
			for _, slot := range openOf(slots) {
				at := e.declarationChangedAt(l, slot)
				if !at.IsZero() && declarationAge(at, time.Now()) >= int64(workingWithin/time.Second) {
					return "stalled"
				}
			}
			return "declared"
		}
		return "working"
	}
	return "waiting"
}

// workingWithin is how recent the last sign of an agent must be for its open
// declaration to read as work in progress rather than merely declared.
const workingWithin = 30 * time.Minute

// slotsOf is the agent's declarations in slot-id order, so a notice reads the
// same every time.
func slotsOf(l *core.Agent) []core.Slot {
	out := make([]core.Slot, 0, len(l.Slots))
	for _, s := range l.Slots {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sortedAgentIDs, so a tick visits agents in the same order every time.
func sortedAgentIDs(s *core.State) []string {
	ids := make([]string, 0, len(s.Agents))
	for id := range s.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
