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
		name: "desktop-approval-confirmation/v1",
		fields: []string{
			"core.State.DesktopApproval", "core.DesktopApprovalSetting.Enabled",
			"core.DesktopApprovalSetting.By", "core.DesktopApprovalSetting.At",
		},
		present: func(st *core.State) bool {
			return st.DesktopApproval.Enabled || st.DesktopApproval.By != "" || !st.DesktopApproval.At.IsZero()
		},
	},
	{
		name: "mailbox-recovery/v1",
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
		present: hasRecoverySnapshotState,
	},
}

func hasRecoverySnapshotState(st *core.State) bool {
	if len(st.RecoveredAddresses) > 0 {
		return true
	}
	for _, agent := range st.Agents {
		if agent != nil && len(agent.InheritedDeclarations) > 0 {
			return true
		}
	}
	for _, message := range st.Messages {
		if message != nil && message.Recovery != nil {
			return true
		}
	}
	return false
}

func requiredSnapshotFeatures(st *core.State) []string {
	var names []string
	for _, feature := range snapshotFeatures {
		if feature.present(st) {
			names = append(names, feature.name)
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
		if !slices.ContainsFunc(snapshotFeatures, func(f snapshotFeature) bool { return f.name == name }) {
			return fmt.Errorf("snapshot requires feature %q; upgrade dibd before opening this board", name)
		}
	}
	return nil
}
