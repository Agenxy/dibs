// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// ChatGPTAppEpoch observes only the app process incarnation. Unlike thread
// ownership, it does not inspect open files or load any thread. A failed or
// ambiguous probe is unknown, never evidence of an app restart.
func ChatGPTAppEpoch() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	processes, err := appProcessPass(ctx)
	if err != nil {
		return "", false
	}
	state, _, err := processes.appEvidence()
	if err != nil {
		return "", false
	}
	// Ownership may conservatively retain readable bundle remnants, but only
	// the main executable establishes an app incarnation. A helper surviving
	// Quit is absence, not a return that can start a reload pass.
	if !processes.mainAppRunning() {
		return "absent", true
	}
	if state.Epoch == "" || state.Epoch == "absent" {
		return state.Epoch, state.Epoch != ""
	}
	sum := sha256.Sum256([]byte(state.Epoch))
	return hex.EncodeToString(sum[:]), true
}
