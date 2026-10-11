// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"slices"

	"github.com/agenxy/dibs/internal/core"
)

// dropRestatedMilestones removes milestones an approval repeats exactly from
// the request it approves. Restating the sender's own steps is agreement, not
// a second declaration, and was refused as one: claude-agenxy-labs-0's first
// approve of 89566 bounced on the three steps its sender had already named
// (2026-10-11). Dropped here, before the op is admitted and ledgered, and not
// accepted in the fold: an older daemon replaying an approval that carries
// milestones on a request that already has them would refuse it and not boot.
func (e *Engine) dropRestatedMilestones(op *core.Op) {
	if len(op.Milestones) == 0 || op.Kind != core.OpRespond {
		return
	}
	if m, ok := e.state.Messages[op.MsgSerial]; ok && slices.Equal(m.Milestones, op.Milestones) {
		op.Milestones = nil
	}
}
