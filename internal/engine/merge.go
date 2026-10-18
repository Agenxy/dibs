// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// MergeOptions selects the preview/commit path. Existing internal Go callers
// without options retain the historical admin-only entry point.
type MergeOptions struct {
	DryRun bool
	Fence  string
}

// MergeAgents authenticates, verifies and applies on ONE writer trip.
func (e *Engine) MergeAgents(
	ctx context.Context, token, from, into string, options ...MergeOptions,
) (core.Result, error) {
	r, err := e.query(ctx, func() core.Result {
		res, fault := e.mergeLocked(token, from, into, options)
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

func (e *Engine) mergeLocked(token, from, into string, options []MergeOptions) (core.Result, error) {
	now := time.Now()
	a, refused := e.authObserve(token, now)
	if refused != nil {
		return nil, refused["error"].(error)
	}
	op := &core.Op{Kind: core.OpMergeAgents, To: from, MergeInto: into}
	if _, err := e.resolveAgentRefs(op); err != nil {
		return nil, err
	}
	from, into = op.To, op.MergeInto
	if len(options) == 0 {
		if !a.IsAdmin() {
			return nil, core.ErrNotAdmin
		}
		if err := e.state.Admit(op); err != nil {
			return nil, err
		}
		return e.applyAndLedger(op, now)
	}
	if err := e.state.MergeSafety(from, into); err != nil {
		return nil, err
	}
	evidence := e.mergeEvidence(from)
	if err := mergeProcessSafe(from, evidence); err != nil {
		return nil, err
	}
	plan := e.state.MergePlan(from, into, 0)
	e.mergeCredentialPreview(plan, a, into)
	_, eligible := e.coordinatorMergeActor(a, from, into, evidence)
	plan["coordinator_eligible"] = eligible == nil
	if eligible != nil {
		plan["reason"] = eligible.Error()
		plan["human_request"] = core.Result{
			"to": "human", "type": "request",
			"merge": core.MergeRequest{From: from, Into: into, Fence: plan["fence"].(string)},
			"body":  "Please merge the duplicate into its surviving identity",
		}
	}

	opt := options[0]
	if opt.DryRun {
		return plan, nil
	}
	actor, err := e.mergeActor(a, from, into, evidence)
	if err != nil {
		return nil, err
	}
	if opt.Fence == "" || opt.Fence != plan["fence"] {
		return nil, mergeStale()
	}
	op.MergeActor = actor
	if err := e.state.Admit(op); err != nil {
		return nil, err
	}
	return e.applyAndLedger(op, now)
}

func (e *Engine) mergeCredentialPreview(plan core.Result, a *core.Agent, into string) {
	transfer := a.ID == e.humanIdentityLocked() || a.IsAdmin()
	plan["nonce_follows"] = transfer && plan["nonce_mappings"].(int) > 0
	if transfer {
		plan["credential_effect"] = "the fork's credential will open " + into
	} else {
		plan["credential_effect"] = "source-only credentials will refuse with E_SEAT_MERGED; reattach with " +
			into + "'s own nonce"
	}
}

func mergeStale() error {
	return &core.Error{
		Code: "E_MERGE_STALE",
		Msg:  "merge preview is missing or its projected inputs changed; nothing moved",
		Hint: "call merge_agents(dry_run:true) again and pass its fence on the real call",
	}
}

func (e *Engine) mergeActor(
	a *core.Agent, from, into string, evidence core.MergeEvidence,
) (*core.MergeActor, error) {
	authority := ""
	switch {
	case a.ID == e.humanIdentityLocked():
		authority = "human"
	case a.IsAdmin():
		authority = "admin"
	default:
		return e.coordinatorMergeActor(a, from, into, evidence)
	}
	return &core.MergeActor{ID: a.ID, Created: a.CreatedSerial, Authority: authority, Evidence: evidence}, nil
}

func (e *Engine) coordinatorMergeActor(
	a *core.Agent, from, into string, evidence core.MergeEvidence,
) (*core.MergeActor, error) {
	if !a.IsCoordinator() {
		return nil, core.ErrNotCoordinator
	}
	if err := e.state.MergeEligibility(from, into, evidence); err != nil {
		return nil, err
	}
	if from == e.humanRowLocked() || into == e.humanRowLocked() ||
		from == e.dibsRowLocked() || into == e.dibsRowLocked() {
		return nil, &core.Error{
			Code: "E_NOT_PERMITTED",
			Msg:  "reserved identities require the human's own decision",
			Hint: "ask the human to preview this pair on the board",
		}
	}
	if err := e.refuseCoordinatorSelfAdoption(a, &core.Op{AdoptAuthorised: true, To: from, Space: into}); err != nil {
		return nil, &core.Error{
			Code: "E_MERGE_NEEDS_HUMAN", Msg: err.Error(),
			Hint: "use the preview's human_request; the human's Approve performs the merge",
		}
	}
	return &core.MergeActor{ID: a.ID, Created: a.CreatedSerial, Authority: "coordinator", Evidence: evidence}, nil
}

func (e *Engine) mergeEvidence(from string) core.MergeEvidence {
	a := e.state.Agents[from]
	if a == nil {
		return core.MergeEvidence{}
	}
	evidence := core.MergeEvidence{
		Source: a.ID, Created: a.CreatedSerial, PID: a.PID, ProcStart: a.ProcStart,
	}
	if a.Agent != nil {
		evidence.Host = a.Agent.HostID
	}
	// Snapshot, binding and kernel observation are taken on this same writer
	// trip. A reused/live PID is conservatively live, never guessed absent.
	if peer, ok := peerSessionIn(e.peerSnapshot(), sessionsOf(a)); ok && peer.PID != 0 {
		evidence.Known, evidence.Live = true, true
	} else if a.PID == 0 {
		evidence.Known = true
	} else if evidence.Host != "" && e.ownsHost(a) && e.prober != nil {
		evidence.Known, evidence.Live = e.mergePIDObservation(a.PID)
	}
	return evidence
}

func (e *Engine) mergePIDObservation(pid int) (known, live bool) {
	if p, ok := e.prober.(interface{ ObservePID(int) (bool, bool) }); ok {
		live, known = p.ObservePID(pid)
		return known, live
	}
	live = e.prober.Alive(pid)
	return live, live // legacy false does not distinguish absence from an error
}

func mergeProcessSafe(from string, evidence core.MergeEvidence) error {
	if !evidence.Known || evidence.Live {
		return &core.Error{
			Code: "E_AGENT_LIVE",
			Msg:  fmt.Sprintf("%s has a live process or its absence cannot be established", from),
			Hint: "stop the fork's process first; unknown or remote process evidence is not proof of absence",
		}
	}
	return nil
}

func (e *Engine) addAdoptionMergeHint(res core.Result, actor *core.Agent, op *core.Op) {
	if op.Kind != core.OpAdoptAgent || actor == nil {
		return
	}
	into := op.Space
	if into == "" {
		into = actor.ID
	}
	if _, err := e.coordinatorMergeActor(actor, op.To, into, e.mergeEvidence(op.To)); err == nil {
		res["merge_hint"] = fmt.Sprintf("to retire the duplicate too, preview merge_agents(agent:%q, into:%q, dry_run:true)",
			op.To, into)
	}
}
