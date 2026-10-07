// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Decide once on the originating response, not on every sweep. Historical
// operations have no date and therefore keep their original GC semantics.
func (e *Engine) stampReviewRetention(op *core.Op, now time.Time) {
	op.RetainUntil = nil // no ingress caller may choose its retention
	if op.Kind != core.OpRespond && op.Kind != core.OpWithdrawMessage {
		return
	}
	m := e.state.Messages[op.MsgSerial]
	if m == nil {
		return
	}
	// UTC also strips the process-local monotonic clock: replay has only the
	// wall-clock value encoded on disk, so live comparisons use that same value.
	until := now.UTC().Add(24 * time.Hour)
	if op.Kind == core.OpRespond && m.HasUnresolvedReviewFlagsAfter(op.Disposition, op.Milestone) {
		// An unresolved review is kept until correction or acceptance, subject
		// to the existing per-recipient terminal cap and its loss watermark.
		until = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	op.RetainUntil = &until
}
