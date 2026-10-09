// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type declarationTime struct {
	serial uint64
	at     time.Time
}

// Derived from committed slot events, including full replay history before
// ring trimming. Keep only current declarations, never an unbounded serial map.
func (e *Engine) rebuildDeclarationTimes(history []core.Event) {
	e.declarationTimes = map[string]map[string]declarationTime{}
	for _, ev := range history {
		e.observeDeclarationTime(ev)
	}
}

func (e *Engine) observeDeclarationTime(ev core.Event) {
	if e.state == nil {
		return
	}
	l := e.state.Agents[ev.Agent]
	if l == nil || l.Retired() {
		delete(e.declarationTimes, ev.Agent)
		return
	}
	slot, _ := ev.Data["slot_id"].(string)
	if ev.Type == "slot.cleared" {
		delete(e.declarationTimes[ev.Agent], slot)
		if len(e.declarationTimes[ev.Agent]) == 0 {
			delete(e.declarationTimes, ev.Agent)
		}
		return
	}
	if ev.Type != "slot.set" {
		return
	}
	s, ok := l.Slots[slot]
	if !ok || s.UpdatedSerial != ev.Serial {
		return
	}
	if e.declarationTimes == nil {
		e.declarationTimes = map[string]map[string]declarationTime{}
	}
	if e.declarationTimes[ev.Agent] == nil {
		e.declarationTimes[ev.Agent] = map[string]declarationTime{}
	}
	e.declarationTimes[ev.Agent][slot] = declarationTime{ev.Serial, ev.TS.Round(0)}
}

func (e *Engine) declarationChangedAt(l *core.Agent, s core.Slot) time.Time {
	if rec := e.declarationTimes[l.ID][s.ID]; rec.serial == s.UpdatedSerial {
		return rec.at
	}
	// Approved requests are synthetic declarations; their approval time is
	// already recorded. An embedder supplying no replay events gets unknown age,
	// rather than a timestamp invented from its boot or last heartbeat.
	for _, m := range e.obligationsOf(l.ID, time.Now()) {
		if s.UpdatedSerial == m.Serial {
			return m.TerminalAt.Round(0)
		}
	}
	return time.Time{}
}

func declarationAge(at, now time.Time) int64 {
	return max(0, int64(now.Round(0).Sub(at.Round(0))/time.Second))
}

type declarationView struct {
	core.Slot
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	UnchangedForSec *int64     `json:"unchanged_for_s,omitempty"`
}

func (e *Engine) decorateDeclarationAges(row map[string]any, l *core.Agent, now time.Time) {
	slots, ok := row["slots"].([]core.Slot)
	if !ok {
		return
	}
	out := make([]declarationView, 0, len(slots))
	for _, slot := range slots {
		view := declarationView{Slot: slot}
		if at := e.declarationChangedAt(l, slot); !at.IsZero() {
			age := declarationAge(at, now)
			view.UpdatedAt, view.UnchangedForSec = &at, &age
		}
		out = append(out, view)
	}
	row["slots"] = out
}
