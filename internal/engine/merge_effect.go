// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

// Both ordinary sends and human approvals enter here on the writer, before
// applying any part of the requested transition. A stale approval stays open.
func (e *Engine) prepareMergeEffect(op *core.Op, actor *core.Agent) error {
	if actor == nil {
		return nil
	}
	if op.Kind == core.OpSendMessage && op.Merge != nil && e.state.HasIdentifiedSend(actor.ID, op.OpID) {
		return nil // applySend verifies the original, complete digest; no second effect
	}
	effect, ignore, approving := e.mergeEffectFor(op, actor)
	if effect == nil {
		return nil
	}
	if approving && actor.ID != e.humanIdentityLocked() {
		return &core.Error{
			Code: "E_NOT_PERMITTED", Msg: "only the human may approve a merge effect",
			Hint: "send the typed merge request to human; no role grant or terminal command is required",
		}
	}
	if err := e.state.MergeSafety(effect.From, effect.Into); err != nil {
		return err
	}
	evidence := e.mergeEvidence(effect.From)
	if err := mergeProcessSafe(effect.From, evidence); err != nil {
		return err
	}
	if effect.Fence != e.state.MergeFence(effect.From, effect.Into, ignore) {
		return mergeStale()
	}
	if approving {
		op.MergeActor = &core.MergeActor{ID: actor.ID, Created: actor.CreatedSerial, Authority: "human", Evidence: evidence}
	}
	return e.state.Admit(op)
}

func (e *Engine) mergeEffectFor(op *core.Op, actor *core.Agent) (*core.MergeRequest, uint64, bool) {
	if op.Kind == core.OpSendMessage {
		return op.Merge, 0, false
	}
	if op.Kind != core.OpRespond || op.Disposition != "approve" {
		return nil, 0, false
	}
	m := e.state.Messages[op.MsgSerial]
	if m == nil || m.To != actor.ID || m.Terminal() {
		return nil, 0, false
	}
	return m.Merge, m.Serial, m.Merge != nil
}
