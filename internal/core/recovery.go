// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"fmt"
	"time"
)

// RecoveryRequest records survivor consent and the exact coordinator incarnation.
// It grants nothing until that distinct coordinator approves on the writer.
type RecoveryRequest struct {
	From               string `json:"from"`
	Into               string `json:"into"`
	SourceCreated      uint64 `json:"source_created_serial"`
	SurvivorCreated    uint64 `json:"survivor_created_serial"`
	CoordinatorCreated uint64 `json:"coordinator_created_serial"`
	Fence              string `json:"fence"`
}

// InheritedDeclaration is history, deliberately outside Agent.Slots. Nothing
// counting current work, holds or continuations may interpret it as a new claim.
type InheritedDeclaration struct {
	Source          string    `json:"source"`
	SourceCreated   uint64    `json:"source_created_serial"`
	Host            string    `json:"host"`
	RecoveredSerial uint64    `json:"recovered_serial"`
	UpdatedAt       time.Time `json:"updated_at,omitzero"`
	Declaration     Slot      `json:"declaration"`
}

// RecoveredAddress survives source-row retention. Destination incarnation is
// part of the authority: reusing its id must never inherit this mailbox route.
type RecoveredAddress struct {
	Source          string `json:"source"`
	SourceCreated   uint64 `json:"source_created_serial"`
	Into            string `json:"into"`
	SurvivorCreated uint64 `json:"survivor_created_serial"`
	By              string `json:"by"`
	Serial          uint64 `json:"serial"`
}

// MaxInheritedDeclarations retains eight full default declaration sets, without
// truncation. A configured larger single-source allowance still fits.
const MaxInheritedDeclarations = 256

// MaxRecoveredAddresses bounds durable reservations without expiring authority
// when a source row is collected.
const MaxRecoveredAddresses = 4096

// InheritedDeclarationLimit accommodates at least one configured full source.
func (s *State) InheritedDeclarationLimit() int {
	return max(MaxInheritedDeclarations, s.Limits.MaxSlotsPerAgent)
}

// RecoveryEligibility is ingress policy; replay never re-evaluates it.
func (s *State) RecoveryEligibility(
	from, into string, evidence MergeEvidence, sourceHost, survivorHost string,
) error {
	if err := s.MergeSafety(from, into); err != nil {
		return err
	}
	a, b := s.Agents[from], s.Agents[into]
	if !sameRecoveryIdentity(a, b, sourceHost, survivorHost) || !recoverableSource(a) ||
		!b.hasSessionBinding() || b.CurrentSession == "" ||
		!b.LastCoordination.After(a.LastCoordination) || !evidence.Known || evidence.Live {
		return errf("E_RECOVERY_NEEDS_HUMAN",
			"recover with the source credential, or ask human for a reviewed merge; no mailbox changed",
			"recovery needs an unprivileged dormant unbound persistent source and a newer bound survivor "+
				"with matching known host/harness/surface and absent process")
	}
	if err := s.recoveryHistoryCapacity(a, b); err != nil {
		return err
	}
	return s.recoveryAddressCapacity(a, into)
}

func sameRecoveryIdentity(a, b *Agent, sourceHost, survivorHost string) bool {
	return a.Kind == KindPersistent && b.Kind == KindPersistent && a.Agent != nil && b.Agent != nil &&
		sourceHost != "" && sourceHost == survivorHost &&
		a.Agent.Harness != "" && a.Agent.Harness == b.Agent.Harness &&
		a.Agent.Surface != "" && a.Agent.Surface == b.Agent.Surface
}

func recoverableSource(a *Agent) bool {
	return (a.Status == StatusDormant || a.Status == StatusStale) &&
		(a.Role == "" || a.Role == RoleMember) && len(a.Permissions) == 0 &&
		!a.hasSessionBinding() && a.CurrentSession == ""
}

func (s *State) recoveryHistoryCapacity(a, b *Agent) error {
	n := len(a.Slots) + len(a.InheritedDeclarations) + len(b.InheritedDeclarations)
	if n <= s.InheritedDeclarationLimit() {
		return nil
	}
	return errf("E_RECOVERY_HISTORY_FULL",
		"undeclare some of your inherited handles, then preview recover_agent again; source history is intact",
		"recovery would retain %d inherited declarations (%d existing, %d source); limit %d",
		n, len(b.InheritedDeclarations), len(a.Slots)+len(a.InheritedDeclarations), s.InheritedDeclarationLimit())
}

func (s *State) recoveryAddressCapacity(a *Agent, into string) error {
	add := 0
	for _, address := range recoveryAddresses(a) {
		if address == HumanActor || address == RoleCoordinator || address == "dibs" {
			return errf("E_NOT_PERMITTED", "ask human to review the reserved source identity",
				"reserved role address cannot be recovered")
		}
		if _, exists := s.RecoveredAddresses[address]; exists {
			return errf("E_NAME_TAKEN", "ask human to reconcile the reserved recovery address",
				"address %q already reserved", address)
		}
		for _, row := range s.Agents {
			if row.ID != a.ID && row.ID != into && (row.ID == address || row.Name == address) {
				return errf("E_AMBIGUOUS_AGENT",
					"choose an unambiguous source address; ask human to reconcile names first",
					"source address %q is shared with %s", address, row.ID)
			}
		}
		add++
	}
	if len(s.RecoveredAddresses)+add > MaxRecoveredAddresses {
		return errf("E_RECOVERY_ADDRESS_FULL",
			"ask human to review the board's recovery address budget; nothing changed",
			"recovery address budget %d reached", MaxRecoveredAddresses)
	}
	return nil
}

func recoveryAddresses(a *Agent) []string {
	if a.Name == "" || a.Name == a.ID {
		return []string{a.ID}
	}
	return []string{a.ID, a.Name}
}

// RecoveredRecipient is used ONLY for new mail ingress, never for cleanup or
// historical reads. Chained recoveries are followed within the durable budget.
func (s *State) RecoveredRecipient(address string) (string, error) {
	id := address
	for range MaxRecoveredAddresses + 1 {
		r, ok := s.RecoveredAddresses[id]
		if !ok {
			return id, nil
		}
		a := s.Agents[r.Into]
		if a == nil || a.CreatedSerial != r.SurvivorCreated || a.Retired() {
			// A recovered survivor may itself have been recovered; its durable
			// record remains usable even after its row was collected.
			next, chained := s.RecoveredAddresses[r.Into]
			if !chained || next.SourceCreated != r.SurvivorCreated {
				return "", errf("E_AGENT_CLOSED",
					"ask the coordinator to recover or reconcile the retired destination; do not register another claimant",
					"recovered address %q points to retired %s", address, r.Into)
			}
		}
		id = r.Into
	}
	return "", errf("E_BAD_TARGET", "ask human to reconcile recovery address records", "recovery address cycle")
}

func (s *State) admitRecovery(op *Op) error {
	limit := max(s.Limits.MaxNameBytes, s.Limits.MaxIDBytes)
	if err := boundStrings(limit, "recover_from", []string{op.RecoverFrom}); err != nil {
		return err
	}
	if err := admitRecoveryRequest(op); err != nil {
		return err
	}
	if op.Kind == OpSetSlot {
		if actor := s.AgentByToken(op.Token); actor != nil {
			if _, inherited := actor.InheritedDeclarations[op.SlotID]; inherited {
				return errf("E_INHERITED_SLOT",
					"undeclare this history handle, and declare live work in a new slot explicitly",
					"inherited declarations cannot be silently reactivated")
			}
		}
	}
	return s.admitRecoveryAddressReuse(op)
}

func admitRecoveryRequest(op *Op) error {
	r := op.Recovery
	if r == nil {
		return nil
	}
	plain := op.Kind == OpSendMessage && op.MsgType == MsgRequest && op.Merge == nil &&
		op.Grant == "" && op.Adopt == "" && len(op.Milestones) == 0
	identified := r.From != "" && r.Into != "" && r.From != r.Into &&
		r.SourceCreated > 0 && r.SurvivorCreated > 0 && r.CoordinatorCreated > 0 && len(r.Fence) == 71
	if plain && identified {
		return nil
	}
	return errf("E_BAD_ARG", "use recover_agent for a typed recovery request", "invalid recovery request")
}

func (s *State) admitRecoveryAddressReuse(op *Op) error {
	knownNonce := s.Agents[s.Nonces[op.Nonce]] != nil
	if op.Kind == OpRegister && !knownNonce {
		if _, reserved := s.RecoveredAddresses[agentID(s, op.Name)]; reserved {
			return errf("E_NAME_TAKEN", "choose another identity name; recovered ids stay reserved",
				"registration would reuse a recovered id")
		}
	}
	if (op.Kind != OpRegister && op.Kind != OpUpdate) || op.Name == "" {
		return nil
	}
	if _, reserved := s.RecoveredAddresses[op.Name]; !reserved {
		return nil
	}
	if op.Kind == OpRegister && knownNonce {
		return nil
	}
	return errf("E_NAME_TAKEN", "keep your own identity and use recover_agent; recovered addresses remain reserved",
		"name %q is a recovered address", op.Name)
}

// RetiresIdentity distinguishes typed retirement effects from ordinary work:
// their approval performs the effect and must never create an owed-work slot.
func (m *Message) RetiresIdentity() bool { return m.Merge != nil || m.Recovery != nil }

// applyRecovery is solely the fold of an already-admitted approval. It makes
// one serial with the response; it does not probe or apply current policy.
func (s *State) applyRecovery(
	r *RecoveryRequest, actor *MergeActor, changedAt map[string]time.Time, now time.Time,
) (Result, []Event) {
	from, into := s.Agents[r.From], s.Agents[r.Into]
	if into.InheritedDeclarations == nil {
		into.InheritedDeclarations = map[string]InheritedDeclaration{}
	}
	n := 0
	put := func(entry InheritedDeclaration) {
		n++
		into.InheritedDeclarations[fmt.Sprintf("inherited:%d:%d", s.Serial+1, n)] = entry
	}
	for _, id := range sortedKeys(from.InheritedDeclarations) {
		put(from.InheritedDeclarations[id])
	}
	for _, id := range sortedKeys(from.Slots) {
		put(InheritedDeclaration{
			Source: from.ID, SourceCreated: from.CreatedSerial, Host: agentHostID(from),
			RecoveredSerial: s.Serial + 1, UpdatedAt: changedAt[id], Declaration: from.Slots[id],
		})
	}
	from.Slots, from.InheritedDeclarations = nil, nil
	if s.RecoveredAddresses == nil {
		s.RecoveredAddresses = map[string]RecoveredAddress{}
	}
	for _, address := range recoveryAddresses(from) {
		s.RecoveredAddresses[address] = RecoveredAddress{
			Source: from.ID, SourceCreated: from.CreatedSerial, Into: into.ID,
			SurvivorCreated: into.CreatedSerial, By: actor.ID, Serial: s.Serial + 1,
		}
	}
	// Recovery never transfers source-only nonce authority, even for an admin.
	negative := *actor
	negative.Authority = "coordinator"
	res, events := s.mergeSupported(from.ID, into.ID, &negative, now)
	res["inherited_declarations"] = n
	return res, events
}
