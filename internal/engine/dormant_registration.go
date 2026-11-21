// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

// A stray bridge nonce must not fork a dormant identity. Preserve the
// session-only recovery path for Dibs-minted credentials; a chosen credential
// still needs to be presented. This runs before vetting the bindings.
func (e *Engine) prepareDormantRegistration(op *core.Op) error {
	if op.Kind != core.OpRegister || (op.SessionID == "" && op.SessionAlias == "") {
		return nil
	}
	host := e.hostOfOp(op)
	owner := e.state.Nonces[op.Nonce]
	if e.sameDormantSession(e.state.Agents[owner], op, host) {
		return nil // an explicit or resolved recovery nonce already proved ownership
	}
	for _, row := range e.state.Agents {
		if !e.sameDormantSession(row, op, host) {
			continue
		}
		if owner == "" && e.useDormantSessionFallback(op, host) {
			return nil
		}
		return &core.Error{
			Code: "E_NEEDS_NONCE",
			Msg:  "a dormant agent of this name already holds the supplied session",
			Hint: "register with your original nonce or recovery_nonces; if you lost the credential, " +
				"ask the coordinator to recover the original mailbox. A session id does not prove ownership",
		}
	}
	return nil
}

// Ask the same resolver used by the fold. A harness alias can supply the real
// session when the bridge's primary is absent or a process placeholder. The
// normalized request still goes through the ordinary privilege guards.
func (e *Engine) useDormantSessionFallback(op *core.Op, host string) bool {
	candidate := *op
	candidate.Nonce = ""
	target := e.state.ReattachTarget(&candidate)
	if target != nil && op.Nonce == "" {
		return true // an existing nonce-free registration can already reattach
	}
	if target == nil && (op.SessionID == "" || processSessionPlaceholder(op.SessionID)) {
		candidate.SessionID = op.SessionAlias
		target = e.state.ReattachTarget(&candidate)
	}
	if !e.sameDormantSession(target, op, host) || !target.NonceMinted {
		return false
	}
	op.Nonce, op.SessionID = "", candidate.SessionID
	return true
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
