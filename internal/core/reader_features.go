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
)

func admitReaderFeatures(op *Op) error {
	if op.Kind != OpRequireV17Features {
		if len(op.RequiredFeatures) != 0 {
			return errf("E_BAD_ARG", "only the writer's reader-requirement marker carries required_features",
				"invalid reader requirement carrier")
		}
		return nil
	}
	if len(op.RequiredFeatures) == 0 || len(op.RequiredFeatures) > 2 {
		return errf("E_BAD_ARG", "name the required v0.0.17 features once", "invalid reader requirement count")
	}
	for i, feature := range op.RequiredFeatures {
		if feature != FeatureDesktopApproval && feature != FeatureMailboxRecovery ||
			i > 0 && op.RequiredFeatures[i-1] >= feature {
			return errf("E_BAD_ARG", "use the supported sorted v0.0.17 feature set", "invalid reader requirement %q", feature)
		}
	}
	return nil
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
	return features
}

// HasMailboxRecoveryData includes consent, inherited history and retained routes.
func (s *State) HasMailboxRecoveryData() bool {
	if len(s.RecoveredAddresses) > 0 {
		return true
	}
	for _, agent := range s.Agents {
		if agent != nil && len(agent.InheritedDeclarations) > 0 {
			return true
		}
	}
	for _, message := range s.Messages {
		if message != nil && message.Recovery != nil {
			return true
		}
	}
	return false
}
