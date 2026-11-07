// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

func (e *Engine) finishReaderBackstop(
	op *core.Op, result core.Result, before mailhistory.Snapshot, events []core.Event, now time.Time,
) (bool, error) {
	missing := e.missingReaderFeaturesAt(op, result)
	if len(missing) == 0 {
		return false, nil
	}
	// A nested append replaces the native record receipt and reuses capture
	// scratch. The normal predicted path keeps history after the send receipt;
	// only actual fallback consumes this original capture before nesting.
	observed := e.mailSource != nil
	if observed {
		e.mailSource.ObserveMail(before, e.state, op, events)
	}
	if err := e.requireReaderFeatures(missing, now); err != nil {
		return observed, err
	}
	slog.Warn("ledger reader requirement backstop", "op", op.Kind, "features", missing)
	return observed, nil
}

// Inspect the actual affected records independently of the pre-append
// prediction. Already-fenced features require no further inspection.
func (e *Engine) missingReaderFeaturesAt(op *core.Op, result core.Result) []string {
	var missing []string
	if !slices.Contains(e.state.RequiredFeatures, core.FeatureFullBoardPolicy) &&
		(op.Displaced != nil || !e.state.FullBoardSetting.IsZero()) {
		missing = append(missing, core.FeatureFullBoardPolicy)
	}
	if !slices.Contains(e.state.RequiredFeatures, core.FeatureDesktopApproval) && !e.state.DesktopApproval.IsZero() {
		missing = append(missing, core.FeatureDesktopApproval)
	}
	if !slices.Contains(e.state.RequiredFeatures, core.FeatureMailboxRecovery) {
		id, _ := result["agent_id"].(string)
		if e.state.HasMailboxRecoveryAt(op, e.state.Serial, id) {
			missing = append(missing, core.FeatureMailboxRecovery)
		}
	}
	if !slices.Contains(e.state.RequiredFeatures, core.FeatureRecoveryConsent) &&
		e.state.HasRecoveryConsentAt(op, e.state.Serial) {
		missing = append(missing, core.FeatureRecoveryConsent)
	}
	return missing
}

func (e *Engine) initializeReaderFeatures(now time.Time) {
	if err := e.requireReaderFeatures(e.state.FeatureData(), now); err != nil {
		panic(err)
	}
}

func (e *Engine) requireFeaturesForAppend(op *core.Op, now time.Time) error {
	var features []string
	if op.Displaced != nil || op.CapacityHuman != "" || op.HumanMint || op.Kind == core.OpSetFullBoardSetting {
		features = append(features, core.FeatureFullBoardPolicy)
	}
	if op.Recovery != nil || op.RecoveryActor != nil {
		features = append(features, core.FeatureMailboxRecovery)
	}
	if op.Recovery.UsesV3Fence() || op.RecoveryActor != nil && e.state.HasRecoveryConsentAt(op, 0) {
		features = append(features, core.FeatureRecoveryConsent)
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
