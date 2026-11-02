// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"slices"
	"sort"
	"time"
)

// OpRequireV17Features is a reader-generation boundary. Do not reuse it for features
// introduced after v0.0.17: the next generation needs another unknown kind.
// The minimum version is in the kind so pre-fence readers name it on refusal.
const OpRequireV17Features = "require_dibs_v0.0.17"

// Feature names are shared by the writer and snapshot format.
const (
	FeatureDesktopApproval = "desktop-approval-confirmation/v1"
	FeatureMailboxRecovery = "mailbox-recovery/v1"
	FeatureRecoveryConsent = "mailbox-recovery-consent/v3"
)

func admitReaderFeatures(op *Op) error {
	if op.Kind != OpRequireV17Features {
		if len(op.RequiredFeatures) != 0 {
			return errf("E_BAD_ARG", "only the writer's reader-requirement marker carries required_features",
				"invalid reader requirement carrier")
		}
		return nil
	}
	if len(op.RequiredFeatures) == 0 || len(op.RequiredFeatures) > 3 {
		return errf("E_BAD_ARG", "name the required v0.0.17 features once", "invalid reader requirement count")
	}
	for i, feature := range op.RequiredFeatures {
		if !SupportsV17Feature(feature) ||
			i > 0 && op.RequiredFeatures[i-1] >= feature {
			return errf("E_BAD_ARG", "use the supported sorted v0.0.17 feature set", "invalid reader requirement %q", feature)
		}
	}
	return nil
}

// SupportsV17Feature is reader-format knowledge, shared with ledger decoding.
func SupportsV17Feature(feature string) bool {
	return feature == FeatureDesktopApproval || feature == FeatureMailboxRecovery || feature == FeatureRecoveryConsent
}

func (s *State) applyReaderFeatures(op *Op, now time.Time) (Result, []Event, error) {
	changed := false
	for _, feature := range op.RequiredFeatures {
		if !slices.Contains(s.RequiredFeatures, feature) {
			s.RequiredFeatures = append(s.RequiredFeatures, feature)
			changed = true
		}
	}
	if !changed {
		return Result{"ok": true, "changed": false}, nil, nil
	}
	sort.Strings(s.RequiredFeatures)
	events := []Event{}
	s.finish(&events, now)
	return Result{"ok": true, "changed": true}, events, nil
}

// FeatureData is derived from the replayed state, including historical records
// written before requirement markers existed. Reading it never changes history.
func (s *State) FeatureData() []string {
	var features []string
	if !s.DesktopApproval.IsZero() {
		features = append(features, FeatureDesktopApproval)
	}
	if s.HasMailboxRecoveryData() {
		features = append(features, FeatureMailboxRecovery)
	}
	for _, message := range s.Messages {
		if message != nil && message.Recovery.UsesV3Fence() {
			features = append(features, FeatureRecoveryConsent)
			break
		}
	}
	sort.Strings(features)
	return features
}

// HasRecoveryConsentAt is bounded to affected message records. The complete
// census belongs to FeatureData at boot, not every writer operation.
func (s *State) HasRecoveryConsentAt(op *Op, serial uint64) bool {
	if op.Recovery.UsesV3Fence() || s.recoveryConsentMessages([]uint64{serial, op.MsgSerial}) ||
		s.recoveryConsentMessages(op.MsgSerials) {
		return true
	}
	return op.MailboxSerials != nil && s.recoveryConsentMessages(*op.MailboxSerials)
}

func (s *State) recoveryConsentMessages(serials []uint64) bool {
	for _, serial := range serials {
		if message := s.Messages[serial]; message != nil && message.Recovery.UsesV3Fence() {
			return true
		}
	}
	return false
}

// HasMailboxRecoveryData includes consent, inherited history and retained routes.
func (s *State) HasMailboxRecoveryData() bool {
	if len(s.RecoveredAddresses) > 0 {
		return true
	}
	for _, agent := range s.Agents {
		if recoveryAgentData(agent) {
			return true
		}
	}
	for _, message := range s.Messages {
		if recoveryMessageData(message) {
			return true
		}
	}
	return false
}

func recoveryAgentData(agent *Agent) bool {
	return agent != nil && len(agent.InheritedDeclarations) > 0
}

func recoveryMessageData(message *Message) bool {
	return message != nil && message.Recovery != nil
}

// HasMailboxRecoveryAt inspects only the operation's affected records. The
// complete historical census belongs at boot, never on each writer append.
func (s *State) HasMailboxRecoveryAt(op *Op, serial uint64, resultAgentID string) bool {
	if len(s.RecoveredAddresses) > 0 || s.recoveryMessagesAt([]uint64{serial, op.MsgSerial}) ||
		s.recoveryMessagesAt(op.MsgSerials) {
		return true
	}
	if op.MailboxSerials != nil && s.recoveryMessagesAt(*op.MailboxSerials) {
		return true
	}
	named := []string{op.AgentID, op.To, op.MergeInto, op.Adopt, op.Name, op.Space, op.Parent, resultAgentID}
	if s.recoveryAgentsAt(named) {
		return true
	}
	return s.recoveryMessageAgentsAt(op.MsgSerial) ||
		op.Recovery != nil && s.recoveryAgentPair(op.Recovery.From, op.Recovery.Into) ||
		op.Merge != nil && s.recoveryAgentPair(op.Merge.From, op.Merge.Into)
}

func (s *State) recoveryMessagesAt(serials []uint64) bool {
	for _, serial := range serials {
		if recoveryMessageData(s.Messages[serial]) {
			return true
		}
	}
	return false
}

func (s *State) recoveryAgentsAt(ids []string) bool {
	for _, id := range ids {
		if recoveryAgentData(s.Agents[id]) {
			return true
		}
	}
	return false
}

func (s *State) recoveryAgentPair(from, into string) bool {
	return recoveryAgentData(s.Agents[from]) || recoveryAgentData(s.Agents[into])
}

func (s *State) recoveryMessageAgentsAt(serial uint64) bool {
	message := s.Messages[serial]
	if message == nil {
		return false
	}
	if s.recoveryAgentsAt([]string{message.From, message.To, message.Adopt}) {
		return true
	}
	return message.Merge != nil && s.recoveryAgentPair(message.Merge.From, message.Merge.Into)
}
