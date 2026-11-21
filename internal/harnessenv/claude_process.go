// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/procinfo"
)

// ClaudeProcess is a session observed on this host, with the kernel's start
// stamp for the parent rather than the bridge. It carries no agent authority.
type ClaudeProcess struct {
	Session string
	PID     int
	Start   int64
}

// ClaudeProcessForBridge uses a kernel-authenticated local peer PID. The
// caller must compare that PID to the claimed bridge PID before calling.
func ClaudeProcessForBridge(peer int, bridgeStart, session string) (ClaudeProcess, bool) {
	if peer <= 1 || bridgeStart == "" || !core.LooksLikeThreadID(session) {
		return ClaudeProcess{}, false
	}
	bridge, err := procinfo.Inspect(peer)
	if err != nil || bridge.PPID <= 1 || procinfo.Stamp(bridge.Start) != bridgeStart {
		return ClaudeProcess{}, false
	}
	parent, err := procinfo.Inspect(bridge.PPID)
	if err != nil || parent.Start.After(bridge.Start) || !claudeSidecarMatches(parent, session) {
		return ClaudeProcess{}, false
	}
	bridgeLive, err := procinfo.SameProcess(bridge)
	if err != nil || !bridgeLive {
		return ClaudeProcess{}, false
	}
	return ClaudeProcess{Session: session, PID: parent.PID, Start: parent.Start.UnixMilli()}, true
}

func claudeSidecarMatches(parent procinfo.Process, session string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(home, ".claude", "sessions", strconv.Itoa(parent.PID)+".json")
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(path) // #nosec G304 -- the daemon user's harness sidecar at the observed parent PID.
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	var sidecar struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
		ProcStart string `json:"procStart"`
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	return err == nil && json.Unmarshal(data, &sidecar) == nil && sidecar.PID == parent.PID &&
		sidecar.SessionID == session && strings.Join(strings.Fields(sidecar.ProcStart), " ") == procinfo.Stamp(parent.Start)
}
