// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) mailReadUpdates(m *core.Message, reader *core.Agent, includeBody bool) []core.Result {
	if includeBody || m.From != reader.ID || m.To == reader.ID || !m.Terminal() {
		return nil
	}
	return e.unreadMailUpdates(m)
}

func (e *Engine) projectMailRead(m *core.Message, res core.Result, updates []core.Result, now time.Time) core.Result {
	if len(updates) > 0 {
		res["message"] = e.outcomeReadHeader(m, now)
		res["updates"] = updates
		delete(res, "milestone_reviews")
	}
	return res
}

func (e *Engine) unreadMailUpdates(m *core.Message) []core.Result {
	selected := e.outcomeUnits(m)
	if len(selected) == 0 {
		return nil
	}
	reports := make(map[uint64]core.Progress, len(m.Progress))
	for _, p := range m.Progress {
		reports[p.Serial] = p
	}
	updates := make([]core.Result, 0, len(selected))
	for _, unit := range selected {
		update := core.Result{"serial": unit.serial, "kind": unit.kind}
		if !unit.at.IsZero() {
			update["at"] = unit.at
		}
		// Inline delivery normalises whitespace. A full read must keep the
		// original words, including indentation inside a reported code block.
		switch unit.kind {
		case "message.progress":
			p := reports[unit.serial]
			update["body"] = p.Note
			if p.Artifact != "" {
				update["deliverable"] = p.Artifact
			}
			if p.Milestone > 0 {
				update["milestone"] = p.Milestone
				update["milestone_count"] = len(m.Milestones)
			}
		case "message.queue_changed":
			update["position"] = e.state.QueuePosition(m)
			update["priority"] = m.EffectivePriority()
		default:
			update["label"] = "response"
			if m.State == core.MsgStateDone {
				// The ledger stores approval and completion together. Keep that
				// exact response rather than guess a boundary in participant text.
				update["label"] = "response (includes any earlier approval text)"
			}
		}
		updates = append(updates, update)
	}
	return updates
}

func (e *Engine) outcomeReadHeader(m *core.Message, now time.Time) core.Result {
	view := e.queueMessageView(m)
	header := e.compactMail(view, now)
	// Compact the original request, never its current outcome. Agents have
	// always collected answers and delivered work through these message keys.
	for key, value := range map[string]string{
		"response": view.Response, "deliverable": view.Deliverable,
		"expire_detail": view.ExpireDetail, "withdrawal_reason": view.WithdrawalReason,
		"withdrawn_by": view.WithdrawnBy, "queue_priority": view.QueuePriority,
		"queue_by": view.QueueBy, "queue_lock_by": view.QueueLockBy,
	} {
		if value != "" {
			header[key] = value
		}
	}
	for key, value := range map[string]uint64{
		"superseded_by": view.SupersededBy, "queue_changed_serial": view.QueueChangedSerial,
	} {
		if value != 0 {
			header[key] = value
		}
	}
	for key, value := range map[string]time.Time{
		"terminal_at": view.TerminalAt, "queue_changed_at": view.QueueChangedAt,
	} {
		if !value.IsZero() {
			header[key] = value
		}
	}
	if view.QueueRank != 0 {
		header["queue_rank"] = view.QueueRank
	}
	if view.QueueDebt {
		header["queue_debt"] = true
	}
	if view.QueueOrderLocked {
		header["queue_order_locked"] = true
	}
	delete(header, "body")
	delete(header, "body_truncated")
	header["body_omitted"] = true
	header["read_hint"] = fmt.Sprintf("read_mail(msg_serial:%d, include_body:true) returns the full envelope", m.Serial)
	return header
}
