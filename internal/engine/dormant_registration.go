// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

// A current session can identify a duplicate registration without authorizing
// it to recover the mailbox. This decision runs before alias vetting changes
// the session the caller presented.
func (e *Engine) refuseDuplicateDormantSession(op *core.Op) error {
	if op.Kind != core.OpRegister || (op.SessionID == "" && op.SessionAlias == "") {
		return nil
	}
	host := e.hostOfOp(op)
	owner := e.state.Agents[e.state.Nonces[op.Nonce]]
	if e.sameDormantSession(owner, op, host) {
		return nil // an explicit or resolved recovery nonce already proved ownership
	}
	for _, row := range e.state.Agents {
		if e.sameDormantSession(row, op, host) {
			return &core.Error{
				Code: "E_NEEDS_NONCE",
				Msg:  "a dormant agent of this name already holds the supplied session",
				Hint: "register with your original nonce or recovery_nonces; if you lost the credential, " +
					"ask the coordinator to recover the original mailbox. A session id does not prove ownership",
			}
		}
	}
	return nil
}

func (e *Engine) sameDormantSession(row *core.Agent, op *core.Op, host string) bool {
	if row == nil || row.Status != core.StatusDormant || row.Name != op.Name {
		return false
	}
	rowHost := e.canonicalHost(displacementHost(row))
	if host != "" && rowHost != "" && host != rowHost {
		return false
	}
	session := wakeSessionOf(row)
	return session != "" && !processSessionPlaceholder(session) && !row.GuessedSession(session) &&
		(session == op.SessionID || session == op.SessionAlias)
}
