// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"fmt"
	"slices"

	"github.com/agenxy/dibs/internal/core"
)

// Snapshot feature names declare what a reader must understand. Version 2
// rejects version-1-only readers before gob can silently discard new fields;
// its feature list lets later readers reject future unknown requirements too.
type snapshotFeature struct {
	name    string
	fields  []string
	present func(*core.State) bool
}

var snapshotFeatures = []snapshotFeature{
	{
		name:   "native-turn-contact-reason/v1",
		fields: []string{"core.ContactEscalation.Reason"},
		present: func(st *core.State) bool {
			for _, contact := range st.Contacts {
				if contact != nil && contact.Reason != "" {
					return true
				}
			}
			return false
		},
	},
	{
		name: core.FeatureFullBoardPolicy,
		fields: []string{
			"core.State.FullBoardSetting", "core.FullBoardSetting.Value",
			"core.FullBoardSetting.By", "core.FullBoardSetting.At", "core.MergedNonce.Displaced",
		},
		present: (*core.State).HasFullBoardData,
	},
	{
		name:    "ledger-requirements/v1",
		fields:  []string{"core.State.RequiredFeatures"},
		present: func(st *core.State) bool { return len(st.RequiredFeatures) > 0 },
	},
	{
		name: core.FeatureDesktopApproval,
		fields: []string{
			"core.State.DesktopApproval", "core.DesktopApprovalSetting.Enabled",
			"core.DesktopApprovalSetting.By", "core.DesktopApprovalSetting.At",
		},
		present: func(st *core.State) bool {
			return !st.DesktopApproval.IsZero()
		},
	},
	{
		name: core.FeatureMailboxRecovery,
		fields: []string{
			"core.State.RecoveredAddresses", "core.Agent.InheritedDeclarations", "core.Message.Recovery",
			"core.RecoveredAddress.Source", "core.RecoveredAddress.SourceCreated", "core.RecoveredAddress.Into",
			"core.RecoveredAddress.SurvivorCreated", "core.RecoveredAddress.By", "core.RecoveredAddress.Serial",
			"core.InheritedDeclaration.Source", "core.InheritedDeclaration.SourceCreated", "core.InheritedDeclaration.Host",
			"core.InheritedDeclaration.RecoveredSerial", "core.InheritedDeclaration.UpdatedAt",
			"core.InheritedDeclaration.Declaration",
			"core.RecoveryRequest.From", "core.RecoveryRequest.Into", "core.RecoveryRequest.SourceCreated",
			"core.RecoveryRequest.SurvivorCreated", "core.RecoveryRequest.CoordinatorCreated", "core.RecoveryRequest.Fence",
		},
		present: (*core.State).HasMailboxRecoveryData,
	},
}

// configuredSnapshotFields are written into a snapshot and never read back
// from one: loadSnapshot replaces the limits with this daemon's own config, so
// an older reader that drops one of these loses nothing it would have used.
// They need no reader feature, which would only make that reader refuse a
// snapshot it can read correctly. TestLoadedSnapshotKeepsTheConfiguredLimits
// is what makes the claim true; a field belongs here only while it holds.
var configuredSnapshotFields = []string{"core.Limits.BlobMaxAge"}

func requiredSnapshotFeatures(st *core.State) []string {
	var names []string
	for _, feature := range snapshotFeatures {
		if feature.present(st) {
			names = append(names, feature.name)
		}
	}
	for _, features := range [][]string{st.RequiredFeatures, st.FeatureData()} {
		for _, name := range features {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

func snapshotHeaderForState(h SnapshotHeader, st *core.State) SnapshotHeader {
	h.Features = requiredSnapshotFeatures(st)
	h.Version = snapshotVersion
	if len(h.Features) > 0 {
		h.Version = featureSnapshotVersion
	}
	return h
}

func validateSnapshotFeatures(h SnapshotHeader) error {
	if h.Version != snapshotVersion && h.Version != featureSnapshotVersion {
		return fmt.Errorf("snapshot version %d is not one this build reads (1 or 2); "+
			"upgrade dibd before opening this board", h.Version)
	}
	if h.Version == snapshotVersion && len(h.Features) > 0 || h.Version == featureSnapshotVersion && len(h.Features) == 0 {
		return fmt.Errorf("snapshot version and declared features disagree; use a build that understands this snapshot")
	}
	seen := map[string]bool{}
	for _, name := range h.Features {
		if seen[name] {
			return fmt.Errorf("snapshot feature %q is declared twice", name)
		}
		seen[name] = true
		declared := slices.ContainsFunc(snapshotFeatures, func(f snapshotFeature) bool { return f.name == name })
		if !core.SupportsV17Feature(name) && !declared {
			return fmt.Errorf("snapshot requires feature %q; upgrade dibd before opening this board", name)
		}
	}
	return nil
}
