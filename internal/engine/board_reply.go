// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

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
	b := e.state.Board()
	e.decorateFolderAccess(b)
	res["board"] = b
}
