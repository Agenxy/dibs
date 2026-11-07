// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

const (
	FullBoardSettingKey   = "limits.full_board"
	FullBoardDisplaceIdle = "displace_idle"
	FullBoardRefuse       = "refuse"
	ProcessDead           = "dead"
	ProcessUnknown        = "unknown"
)

// AgentDisplacement records the engine's observed ranking key and exact
// victim. Replay never probes, re-ranks, or asks which process is alive now.
type AgentDisplacement struct {
	ID            string    `json:"id"`
	CreatedSerial uint64    `json:"created_serial"`
	LastActivity  time.Time `json:"last_activity"`
	Process       string    `json:"process"`
	Human         string    `json:"human,omitempty"`
}

type FullBoardSetting struct {
	Value string    `json:"value,omitempty"`
	By    string    `json:"by,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

func (s FullBoardSetting) IsZero() bool { return s.Value == "" && s.By == "" && s.At.IsZero() }

func ValidFullBoardPolicy(v string) bool { return v == FullBoardDisplaceIdle || v == FullBoardRefuse }

func (s *State) HasFullBoardData() bool {
	if !s.FullBoardSetting.IsZero() {
		return true
	}
	for _, nonce := range s.MergedNonces {
		if nonce.Displaced {
			return true
		}
	}
	return false
}

func (s *State) EffectiveFullBoardPolicy(fallback string) string {
	if s.FullBoardSetting.Value != "" {
		return s.FullBoardSetting.Value
	}
	if fallback != "" {
		return fallback
	}
	return FullBoardDisplaceIdle
}

// A nonce bound to a missing row still counts as known: let the fold diagnose
// the credential, never close somebody else first.
func (s *State) KnownRegistration(op *Op) bool {
	if op.Nonce != "" {
		_, known := s.Nonces[op.Nonce]
		return known
	}
	return s.ReattachTarget(op) != nil
}

// RegistrationCapacity reports binding ceilings for NEW identities. Recovery
// keeps the fold's historical acceptance/refusal rules, which precede its limit
// checks even for archived rows. It must never displace another identity.
func (s *State) RegistrationCapacity(op *Op) (total, persistent bool) {
	kind := op.AgentKind
	if s.KnownRegistration(op) {
		return false, false // the fold diagnoses missing/closed credential targets
	}
	live, standing := 0, 0
	for _, row := range s.Agents {
		if row.Status == StatusActive || row.Sleeping() {
			live++
			if row.Kind == KindPersistent {
				standing++
			}
		}
	}
	return live >= s.Limits.MaxAgents, kind == KindPersistent && standing >= s.Limits.MaxPersistentAgents
}

func (s *State) admitFullBoard(op *Op) error {
	if op.Kind == OpSetFullBoardSetting {
		if op.SettingKey != FullBoardSettingKey || !ValidFullBoardPolicy(op.SettingValue) {
			return errf("E_BAD_SETTING", "use limits.full_board with displace_idle or refuse", "invalid full-board policy")
		}
		actor := s.AgentByToken(op.Token)
		if actor == nil || actor.Gone() {
			return ErrBadToken
		}
		if !actor.IsCoordinator() {
			return ErrNotCoordinator
		}
		return nil
	}
	if op.Kind != OpRegister {
		if op.Displaced != nil || op.FullBoardPolicy != "" {
			return errf("E_BAD_ARG", "only register records a capacity decision", "invalid displacement carrier")
		}
		return nil
	}
	if op.FullBoardPolicy != "" && !ValidFullBoardPolicy(op.FullBoardPolicy) {
		return errf("E_BAD_SETTING", "use displace_idle or refuse", "invalid registration policy")
	}
	if op.FullBoardPolicy == "" && op.Displaced == nil {
		return nil // legacy ingress; modern engine admission sets the policy late
	}
	total, persistent := s.RegistrationCapacity(op)
	if d := op.Displaced; d != nil {
		victim := s.Agents[d.ID]
		if op.FullBoardPolicy != FullBoardDisplaceIdle || (!total && !persistent) || s.KnownRegistration(op) ||
			victim == nil || victim.Status != StatusDormant || victim.IsCoordinator() ||
			d.CreatedSerial != victim.CreatedSerial || d.LastActivity.Before(victim.LastCoordination) ||
			(d.Process != ProcessDead && d.Process != ProcessUnknown) ||
			(persistent && victim.Kind != KindPersistent) || len(d.Human) > s.Limits.MaxIDBytes {
			return errf("E_BAD_DISPLACEMENT", "retry register so the writer can observe an eligible idle seat",
				"invalid recorded displacement")
		}
		return nil
	}
	if !total && !persistent {
		return nil
	}
	if op.FullBoardPolicy == FullBoardDisplaceIdle {
		ceiling, limit := "agents", s.Limits.MaxAgents
		if persistent {
			ceiling, limit = "persistent agents", s.Limits.MaxPersistentAgents
		}
		return errf("E_AGENT_LIMIT", "no eligible dormant seat remains: relevant rows are not dormant, have verified-alive processes, or are protected. "+
			"Verified-alive processes, the human, coordinators and admins are protected; verified-dead rows rank before "+
			"unknown processes, then by oldest activity and id. Retry register, or ask the human to free a seat or "+
			"raise the relevant limit in dibs.toml; settings lists limits.full_board and its setter",
			"this board holds its maximum of %d %s", limit, ceiling)
	}
	return s.registrationLimitError(total)
}

// AdmitFullBoard is the final capacity gate after the engine has resolved
// credential recovery and kind defaults. It remains ingress-only.
func (s *State) AdmitFullBoard(op *Op) error { return s.admitFullBoard(op) }

// Keep the historical refusal text shared with old Apply folds.
func (s *State) registrationLimitError(total bool) error {
	if total {
		return errf("E_AGENT_LIMIT", "sign_off the agents that have finished, or ask the human to raise max_agents in dibs.toml",
			"this board holds its maximum of %d live agents", s.Limits.MaxAgents)
	}
	return errf("E_AGENT_LIMIT", "a standing identity holds its slot while dormant, which is the point of one, so this usually means siblings accumulated: an agent that could not prove it was itself and registered again. Reclaim them with adopt_agent, or ask the human to raise max_persistent_agents in dibs.toml. Registering as ephemeral works now and keeps no mailbox",
		"this board holds its maximum of %d persistent agents (of %d agents in total, so it is the PERSISTENT ceiling you have hit)",
		s.Limits.MaxPersistentAgents, s.Limits.MaxAgents)
}

func (s *State) applyFullBoardSetting(actor *Agent, op *Op, now time.Time) (Result, []Event) {
	if s.FullBoardSetting.Value == op.SettingValue {
		return Result{"setting": FullBoardSettingKey, "value": op.SettingValue,
			"by": s.FullBoardSetting.By, "saved": true, "changed": false}, nil
	}
	s.FullBoardSetting = FullBoardSetting{Value: op.SettingValue, By: actor.ID, At: now}
	return Result{"setting": FullBoardSettingKey, "value": op.SettingValue, "by": actor.ID, "saved": true, "changed": true},
		[]Event{{Type: "setting.changed", Agent: actor.ID, Data: map[string]any{"setting": FullBoardSettingKey, "value": op.SettingValue}}}
}

func (s *State) closeDisplacedAgent(d *AgentDisplacement, newcomer string, now time.Time) []Event {
	victim := s.Agents[d.ID] // Admit established the incarnation and eligibility.
	undelivered, unanswered := 0, 0
	for _, message := range s.Messages {
		if message.To != victim.ID || message.Terminal() {
			continue
		}
		if message.DeliveredAt == 0 {
			undelivered++
		}
		if message.Expecting() {
			unanswered++
		}
	}
	_, events := s.applyClose(victim, now)
	if s.MergedNonces == nil {
		s.MergedNonces = map[string]MergedNonce{}
	}
	for nonce, id := range s.Nonces {
		if id == victim.ID {
			s.MergedNonces[nonce] = MergedNonce{From: victim.ID, Into: newcomer,
				By: FullBoardSettingKey, Serial: s.Serial + 1, Displaced: true}
			delete(s.Nonces, nonce)
		}
	}
	return append(events, Event{Type: "agent.displaced", Agent: victim.ID, To: d.Human,
		Data: map[string]any{"victim": victim.ID, "newcomer": newcomer,
			"process": d.Process, "last_activity": d.LastActivity, "created_serial": d.CreatedSerial,
			"undelivered_mail": undelivered, "expired_asks": unanswered}})
}
