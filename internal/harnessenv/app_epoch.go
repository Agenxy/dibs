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
	out, err := appProbeOutput(ctx, "/bin/ps", "-axo", "pid=,lstart=,comm=")
	if err != nil || len(out) > 4<<20 {
		return "", false
	}
	state, _ := appProcessEvidence(string(out))
	if state.Epoch == "" || state.Epoch == "absent" {
		return state.Epoch, state.Epoch != ""
	}
	sum := sha256.Sum256([]byte(state.Epoch))
	return hex.EncodeToString(sum[:]), true
}
