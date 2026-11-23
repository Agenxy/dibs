// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// ChatGPTAppEpoch observes only the app process incarnation. Unlike thread
// ownership, it needs only the main executable. The idle poll uses native
// path evidence. An unreadable main candidate leaves the epoch unknown.
func ChatGPTAppEpoch() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	processes, err := appProcessPass(ctx)
	if err != nil {
		return "", false
	}
	mainPID := 0
	for pid, row := range processes.rows {
		// Legacy and fixture rows already carry their path. Native rows use
		// comm only to select candidates, then prove the executable path.
		if row.kernel.PID != 0 && row.comm != "ChatGPT" {
			continue
		}
		path := processes.readPath(pid, false)
		if path.state == processUnknown {
			return "", false
		}
		if path.state == processPresent && path.value == chatGPTRoot+"Contents/MacOS/ChatGPT" {
			mainPID = max(mainPID, pid)
		}
	}
	if mainPID == 0 {
		return "absent", true
	}
	epoch := strconv.Itoa(mainPID) + ":" + processes.rows[mainPID].start
	sum := sha256.Sum256([]byte(epoch))
	return hex.EncodeToString(sum[:]), true
}
