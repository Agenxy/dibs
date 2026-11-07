// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

var errRegisterRepick = errors.New("registration capacity observation changed")

type fullBoardDefault struct{ value, source string }

func (e *Engine) SetFullBoardPolicyDefault(value string) error {
	source := "dibs.toml"
	if value == "" {
		value, source = core.FullBoardDisplaceIdle, "default"
	}
	if !core.ValidFullBoardPolicy(value) {
		return fmt.Errorf("limits.full_board: use displace_idle or refuse")
	}
	e.fullBoardDefault.Store(&fullBoardDefault{value, source})
	return nil
}

func (e *Engine) fullBoardPolicy() (string, string) {
	value, source := core.FullBoardDisplaceIdle, "default"
	if configured := e.fullBoardDefault.Load(); configured != nil {
		value, source = configured.value, configured.source
	}
	return e.state.EffectiveFullBoardPolicy(value), source
}

type displacementEvidence struct {
	recorded core.AgentDisplacement
	pid      int
	start    int64
	host     string
	local    bool
}

type registerCapacityPlan struct {
	needed bool
	choice *displacementEvidence
}

// The writer never probes. The query captures eligible incarnations; liveness
// is observed here, after leaving the loop, and the selected row is checked
// again before Apply. A definite pre-apply race permits exactly one re-pick.
func (e *Engine) registerWithCapacity(ctx context.Context, original *core.Op) (core.Result, error) {
	for pick := 0; pick < 2; pick++ {
		op := *original
		plan, err := e.observeRegisterCapacity(ctx, &op)
		if err != nil {
			return nil, err
		}
		result, err := e.send(ctx, request{op: &op, capacity: plan, reply: make(chan reply, 1),
			receipt: sendReceiptFrom(ctx), attempt: sendAttemptFrom(ctx), prepaid: fanoutPrepaid(ctx)})
		if errors.Is(err, errRegisterRepick) {
			continue // no Apply or append occurred
		}
		*original = op
		return result, err
	}
	return nil, &core.Error{Code: "E_AGENT_LIMIT", Msg: "idle seats changed during both registration attempts",
		Hint: "retry register; the writer re-picks only once and never displaces an agent that woke or changed"}
}

func displacementHost(agent *core.Agent) string {
	if agent.Agent != nil {
		return agent.Agent.HostID
	}
	return ""
}

func (e *Engine) observeRegisterCapacity(ctx context.Context, incoming *core.Op) (*registerCapacityPlan, error) {
	plan := &registerCapacityPlan{}
	var candidates []displacementEvidence
	_, err := e.query(ctx, func() core.Result {
		op := *incoming
		op.V7Semantics = true
		if op.AgentKind == "" {
			op.AgentKind = core.KindPersistent
		}
		if err := e.state.MergedNonceError(op.Nonce); err != nil {
			return core.Result{"error": err}
		}
		policy, _ := e.fullBoardPolicy()
		total, persistent := e.state.RegistrationCapacity(&op)
		if policy != core.FullBoardDisplaceIdle || (!total && !persistent) ||
			e.state.KnownRegistration(&op) || len(op.RecoveryNonces) != 0 {
			return core.Result{"ok": true}
		}
		plan.needed = true
		for _, row := range e.state.Agents {
			if row.Status != core.StatusDormant || row.IsCoordinator() || e.isTheHuman(row.ID) ||
				(persistent && row.Kind != core.KindPersistent) {
				continue
			}
			local := e.ownsHost(row)
			if local && e.sessionMovedProcess(row) {
				continue // live harness evidence is stronger than an old PID
			}
			activity, _ := e.evidenceWithSource(row, false)
			candidates = append(candidates, displacementEvidence{
				recorded: core.AgentDisplacement{ID: row.ID, CreatedSerial: row.CreatedSerial, LastActivity: activity},
				pid:      row.PID, start: row.ProcStart, host: displacementHost(row), local: local,
			})
		}
		return core.Result{"ok": true}
	})
	if err != nil {
		return nil, err
	}
	eligible := candidates[:0]
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate.recorded.Process = core.ProcessUnknown
		if candidate.local && candidate.pid != 0 && e.prober != nil {
			if e.prober.Alive(candidate.pid) {
				continue
			}
			candidate.recorded.Process = core.ProcessDead
		}
		eligible = append(eligible, candidate)
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := eligible[i].recorded, eligible[j].recorded
		if a.Process != b.Process {
			return a.Process == core.ProcessDead
		}
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.Before(b.LastActivity)
		}
		return a.ID < b.ID
	})
	if len(eligible) != 0 {
		choice := eligible[0]
		plan.choice = &choice
	}
	return plan, nil
}

func (e *Engine) admitRegisterCapacity(op *core.Op, plan *registerCapacityPlan) error {
	op.FullBoardPolicy, _ = e.fullBoardPolicy()
	op.Displaced = nil
	total, persistent := e.state.RegistrationCapacity(op)
	if op.FullBoardPolicy == core.FullBoardDisplaceIdle && (total || persistent) && !e.state.KnownRegistration(op) {
		if plan == nil || !plan.needed {
			return errRegisterRepick
		}
		if picked := plan.choice; picked != nil {
			row := e.state.Agents[picked.recorded.ID]
			if !e.displacementStillValid(row, picked, persistent) {
				return errRegisterRepick
			}
			recorded := picked.recorded
			recorded.Human = e.humanRowLocked()
			op.Displaced = &recorded
		}
	}
	return e.state.AdmitFullBoard(op)
}

func (e *Engine) displacementStillValid(row *core.Agent, picked *displacementEvidence, persistent bool) bool {
	if row == nil || row.CreatedSerial != picked.recorded.CreatedSerial || row.Status != core.StatusDormant ||
		row.IsCoordinator() || e.isTheHuman(row.ID) || (persistent && row.Kind != core.KindPersistent) ||
		row.PID != picked.pid || row.ProcStart != picked.start || displacementHost(row) != picked.host ||
		e.ownsHost(row) != picked.local || (picked.local && e.sessionMovedProcess(row)) {
		return false
	}
	activity, _ := e.evidenceWithSource(row, false)
	return activity.Equal(picked.recorded.LastActivity)
}

// Called only after the displacement's append, never during replay. The event
// is the durable audit; an informational native alert contains metadata only.
func (e *Engine) notifyDisplacedMail(ev core.Event) {
	unread, _ := ev.Data["undelivered_mail"].(int)
	asks, _ := ev.Data["expired_asks"].(int)
	if unread == 0 && asks == 0 {
		return
	}
	n := HumanNotice{Serial: ev.Serial, Node: e.state.NodeID, Type: core.MsgNotify,
		From: "dibs", FromName: "Dibs", Body: fmt.Sprintf(
			"Recycled dormant seat %s to admit %s: %d undelivered mail item(s), %d unanswered ask(s) expired. Inspect the old mailbox with all_mail as an admin.",
			ev.Data["victim"], ev.Data["newcomer"], unread, asks)}
	if e.enqueueHumanNotice(n) > 0 {
		return
	}
	available, ask := e.localHumanNotifier()
	e.humanDelivery.mu.Lock()
	profile := e.humanDelivery.nativeProfile
	e.humanDelivery.mu.Unlock()
	go func() {
		if !available() {
			return
		}
		_, err := ask(humanask.Message{Serial: n.Serial, Node: n.Node, Type: n.Type,
			From: n.From, FromName: n.FromName, Body: n.Body, NotificationProfile: profile})
		if err != nil {
			slog.Warn("displaced-mail notice could not be posted; audit remains on the board", "serial", n.Serial, "err", err)
		}
	}()
}
