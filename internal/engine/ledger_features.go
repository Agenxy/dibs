// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"slices"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) initializeReaderFeatures(now time.Time) {
	if err := e.requireReaderFeatures(e.state.FeatureData(), now); err != nil {
		panic(err)
	}
}

func (e *Engine) requireFeaturesForAppend(op *core.Op, now time.Time) error {
	var features []string
	if op.Recovery != nil || op.RecoveryActor != nil {
		features = append(features, core.FeatureMailboxRecovery)
	}
	if op.Kind == core.OpSetDesktopApprovalSetting &&
		(op.SettingValue == "true") != e.state.DesktopApproval.Enabled {
		features = append(features, core.FeatureDesktopApproval)
	}
	return e.requireReaderFeatures(features, now)
}

func (e *Engine) requireReaderFeatures(features []string, now time.Time) error {
	var missing []string
	for _, feature := range features {
		if !slices.Contains(e.state.RequiredFeatures, feature) {
			missing = append(missing, feature)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	op := &core.Op{Kind: core.OpRequireV17Features, RequiredFeatures: missing}
	// Old folds authenticate before diagnosing an unknown actor op. Recording
	// a retained non-closed actor makes their error name the version-bearing
	// kind. The current marker neither acts as that agent nor touches it.
	var ids []string
	for id, agent := range e.state.Agents {
		if agent != nil && agent.Status != core.StatusClosed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		op.AgentID = ids[0]
	}
	if err := e.state.Admit(op); err != nil {
		return err
	}
	_, err := e.applyAndLedger(op, now)
	return err
}
