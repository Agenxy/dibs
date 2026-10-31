// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// RecoverAgent never accepts a destination: the authenticated caller is the
// survivor. Preview, process observation and request admission share one trip.
func (e *Engine) RecoverAgent(ctx context.Context, token, from string, opt MergeOptions) (core.Result, error) {
	r, err := e.query(ctx, func() core.Result {
		res, fault := e.recoverLocked(token, from, opt)
		if fault != nil {
			return core.Result{"error": fault}
		}
		return res
	})
	if err != nil {
		return nil, err
	}
	if fault, ok := r["error"].(error); ok {
		return nil, fault
	}
	return r, nil
}

func (e *Engine) recoverLocked(token, from string, opt MergeOptions) (core.Result, error) {
	if len(from) > max(e.state.Limits.MaxIDBytes, e.state.Limits.MaxNameBytes) {
		return nil, &core.Error{
			Code: "E_TOO_LARGE", Msg: "source reference is too large",
			Hint: "use the source's exact id from the board",
		}
	}
	now := time.Now()
	survivor, refused := e.authObserve(token, now)
	if refused != nil {
		return nil, refused["error"].(error)
	}
	op := &core.Op{Kind: core.OpMergeAgents, To: from}
	if _, err := e.resolveAgentRefs(op); err != nil {
		return nil, err
	}
	from = op.To
	lead := e.state.Agents[e.state.CoordinatorID()]
	if lead == nil {
		return nil, core.ErrNoCoordinator
	}
	effect := &core.RecoveryRequest{
		From: from, Into: survivor.ID, SurvivorCreated: survivor.CreatedSerial,
		CoordinatorCreated: lead.CreatedSerial,
	}
	if source := e.state.Agents[from]; source != nil {
		effect.SourceCreated = source.CreatedSerial
	}
	if _, err := e.recoveryDecision(effect, lead); err != nil {
		return nil, err
	}
	plan := e.state.MergePlan(from, survivor.ID, 0)
	plan["claims"] = len(plan["claims"].([]*core.Claim))
	plan["spaces"] = len(plan["spaces"].([]string))
	source := e.state.Agents[from]
	plan["inherited_declarations"] = len(source.Slots) + len(source.InheritedDeclarations)
	plan["existing_inherited_declarations"] = len(survivor.InheritedDeclarations)
	plan["inherited_limit"] = e.state.InheritedDeclarationLimit()
	plan["routing_effect"] = fmt.Sprintf(
		"After coordinator approval, new mail addressed to %s (id %s) routes to %s (id %s), "+
			"including after source retention. Existing owned mail also moves. Old declarations become inactive "+
			"inherited history; your current slots stay unchanged.", source.Name, source.ID, survivor.Name, survivor.ID)
	plan["credential_effect"] = "source-only credentials refuse with E_SEAT_MERGED; " +
		"only your own survivor nonce opens the survivor"
	plan["coordinator"] = lead.ID
	if opt.DryRun {
		return plan, nil
	}
	if opt.Fence == "" || opt.Fence != plan["fence"] {
		return nil, recoveryStale()
	}
	effect.Fence = opt.Fence
	return e.exec(&core.Op{
		Kind: core.OpSendMessage, Token: token, To: lead.ID, MsgType: core.MsgRequest,
		Body: plan["routing_effect"].(string), Recovery: effect,
	}, now)
}

func recoveryStale() error {
	return &core.Error{
		Code: "E_RECOVERY_STALE", Msg: "recovery inputs changed; nothing moved",
		Hint: "call recover_agent(dry_run:true) again and commit its fresh fence",
	}
}

func (e *Engine) recoveryDecision(r *core.RecoveryRequest, coordinator *core.Agent) (*core.MergeActor, error) {
	if coordinator == nil || !coordinator.IsCoordinator() {
		return nil, core.ErrNotCoordinator
	}
	from, into := e.state.Agents[r.From], e.state.Agents[r.Into]
	if !recoveryIncarnations(r, from, into, coordinator) {
		return nil, recoveryStale()
	}
	if err := e.recoveryCustody(from, into, coordinator); err != nil {
		return nil, err
	}
	evidence := e.mergeEvidence(from.ID)
	if err := mergeProcessSafe(from.ID, evidence); err != nil {
		return nil, err
	}
	sourceHost, survivorHost := "", ""
	if from.Agent != nil {
		sourceHost = e.canonicalHost(from.Agent.HostID)
	}
	if into.Agent != nil {
		survivorHost = e.canonicalHost(into.Agent.HostID)
	}
	evidence.Host = sourceHost
	if err := e.state.RecoveryEligibility(from.ID, into.ID, evidence, sourceHost, survivorHost); err != nil {
		return nil, err
	}
	return &core.MergeActor{
		ID: coordinator.ID, Created: coordinator.CreatedSerial, Authority: "coordinator", Evidence: evidence,
	}, nil
}

// Both request and approval use the ordinary production send/respond pipeline.
// No grant or mail read occurs when the survivor records consent.
func (e *Engine) prepareRecoveryEffect(op *core.Op, actor *core.Agent) error {
	if actor == nil {
		return nil
	}
	r, ignore, approving := op.Recovery, uint64(0), false
	if op.Kind == core.OpRespond && op.Disposition == "approve" {
		m := e.state.Messages[op.MsgSerial]
		if m != nil && m.To == actor.ID && !m.Terminal() {
			r, ignore, approving = m.Recovery, m.Serial, true
		}
	}
	if r == nil {
		return nil
	}
	coordinator := e.state.Agents[op.To]
	if approving {
		coordinator = actor
	} else if op.Kind != core.OpSendMessage || actor.ID != r.Into {
		return &core.Error{
			Code: "E_NOT_PERMITTED", Msg: "only the survivor can request recovery",
			Hint: "use recover_agent with your own token",
		}
	}
	decision, err := e.recoveryDecision(r, coordinator)
	if err != nil {
		return err
	}
	if r.Fence != e.state.MergeFence(r.From, r.Into, ignore) {
		return recoveryStale()
	}
	if approving {
		op.RecoveryActor = decision
		op.RecoveryDeclarationTimes = map[string]time.Time{}
		from := e.state.Agents[r.From]
		for id, slot := range from.Slots {
			op.RecoveryDeclarationTimes[id] = e.declarationChangedAt(from, slot)
		}
	}
	return e.state.Admit(op)
}

func recoveryIncarnations(r *core.RecoveryRequest, from, into, coordinator *core.Agent) bool {
	return from != nil && into != nil && from.CreatedSerial == r.SourceCreated &&
		into.CreatedSerial == r.SurvivorCreated && coordinator.CreatedSerial == r.CoordinatorCreated
}

func (e *Engine) recoveryCustody(from, into, coordinator *core.Agent) error {
	if from.ID == e.humanRowLocked() || into.ID == e.humanRowLocked() ||
		from.ID == e.dibsRowLocked() || into.ID == e.dibsRowLocked() {
		return &core.Error{
			Code: "E_NOT_PERMITTED", Msg: "reserved identities cannot use recovery",
			Hint: "ask human to review the identity pair",
		}
	}
	// Distinct hands are required even for an admin acting as coordinator.
	// This is the existing recorded provenance rule, not cryptographic lineage.
	if coordinator.ID == into.ID || !coordinator.OtherHands(into) {
		return &core.Error{
			Code: "E_NOT_PERMITTED", Msg: "recovery needs approval by a distinct coordinator",
			Hint: "ask a coordinator from another recorded session, or ask human for a reviewed merge",
		}
	}
	return e.refuseCoordinatorSelfAdoption(coordinator, &core.Op{
		AdoptAuthorised: true, To: from.ID, Space: into.ID,
	})
}
