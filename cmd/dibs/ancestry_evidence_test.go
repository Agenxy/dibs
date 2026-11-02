// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "testing"

const (
	ancestryEvidenceEnv = "DIBS_TEST_ANCESTRY_EVIDENCE"
	ancestryRoleEnv     = "DIBS_TEST_ANCESTRY_ROLE"
)

// Only the Darwin ancestry fixture installs a recorder. Its child helpers are
// shared with other platforms and fixtures, where these calls do nothing.
type ancestryStage uint8

const (
	ancestryHelperEntered ancestryStage = iota
	ancestryAppStarting
	ancestryAppStarted
	ancestryBridgesStarting
	ancestryBridgesStarted
	ancestryRequestDecoded
	ancestryRequestWriteStarting
	ancestryRequestWritten
	ancestryReplyRead
	ancestryReplyWritten
	ancestryHandlerEntered
	ancestryHandlerReturned
	ancestryBridgeRunning
	ancestryBridgeReturned
	ancestryLookupEntered
	ancestryLookupAcquired
	ancestryLookupFailed
	ancestryStageCount
)

type ancestryStages struct{ record func(ancestryStage) }

func (s ancestryStages) note(stage ancestryStage) {
	if s.record != nil {
		s.record(stage)
	}
}

var ancestryFixtureStages = func(_ *testing.T, _ int) ancestryStages { return ancestryStages{} }
