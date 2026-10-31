// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"slices"

	"github.com/agenxy/dibs/internal/core"
)

// The fold returns decisions; only a live caller needs a reply board. Keeping
// projection here means both ordinary and history replay use the ONE Apply
// without paying fleet-sized allocation for every old register/checkpoint.
func (e *Engine) projectReplyBoard(op *core.Op, res core.Result) {
	if res == nil {
		return
	}
	switch op.Kind {
	case core.OpRegister, core.OpResume:
		if res["superseded"] == true {
			return
		}
	case core.OpAckBoard:
	case core.OpActivityCheckpoint:
		if op.MailboxSerials == nil {
			return
		}
	default:
		return
	}
	if actor := e.state.AgentByToken(op.Token); actor != nil {
		if len(actor.InheritedDeclarations) > 0 {
			keys := make([]string, 0, len(actor.InheritedDeclarations))
			for key := range actor.InheritedDeclarations {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			view := []core.Result{}
			for _, key := range keys[:min(8, len(keys))] {
				view = append(view, core.Result{"slot_id": key, "history": actor.InheritedDeclarations[key]})
			}
			res["inherited_declaration_count"], res["inherited_declarations"] = len(keys), view
			res["inherited_hint"] = "History only: no live holds or continuation. " +
				"undeclare(slot_id:<inherited handle>) retires an entry; check_in shows the next eight. " +
				"Declare live work explicitly in a new slot."
		}
		if c := e.state.MailboxCapacity(actor.ID); c.NearCapacity {
			res["mailbox_capacity"] = c
		}
	}
	b := e.state.Board()
	e.decorateFolderAccess(b)
	e.decorateReconnectPending(b)
	res["board"] = b
}
