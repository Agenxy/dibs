// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package antigravity observes Antigravity's existing desktop input route.
package antigravity

import (
	"path/filepath"
	"runtime"
	"time"

	"github.com/agenxy/dibs/internal/procinfo"
)

const (
	// ConversationMeta is the per-call field supplied by the Antigravity app.
	ConversationMeta = "antigravity.google/conversation_id"
	// BridgeMeta reports that the stdio bridge recognized its parent.
	BridgeMeta = "com.dibs/antigravity_bridge"
	// Harness is the canonical Dibs harness name.
	Harness = "antigravity"
	// Client is the harness name on existing Antigravity MCP registrations.
	Client        = "antigravity-client"
	binary        = "/Applications/Antigravity.app/Contents/Resources/bin/language_server"
	appExecutable = "/Applications/Antigravity.app/Contents/MacOS/Antigravity"
)

// Hub identifies one verified incarnation of the desktop app's language server.
// Credentials are reread at delivery and never recorded in an agent binding.
type Hub struct {
	PID   int
	Start time.Time
}

// Bridge recognizes the app's direct stdio MCP child. The daemon calls it on
// its kernel-authenticated local HTTP peer, never on a claimed request PID.
func Bridge(pid int) (Hub, bool) {
	if runtime.GOOS != "darwin" {
		return Hub{}, false
	}
	child, err := procinfo.Inspect(pid)
	if err != nil || child.PPID <= 1 {
		return Hub{}, false
	}
	childArgs, err := procinfo.Command(pid)
	if err != nil || len(childArgs) != 2 || childArgs[1] != "mcp-stdio" || filepath.Base(childArgs[0]) != "dibs" {
		return Hub{}, false
	}
	parent, ok := desktopHub(child.PPID)
	if !ok || parent.Start.After(child.Start) || !procinfo.OwnsStdioPipes(pid, parent.PID) {
		return Hub{}, false
	}
	same, err := procinfo.SameProcess(child)
	if err != nil || !same {
		return Hub{}, false
	}
	_, err = liveDescriptor(parent)
	return parent, err == nil
}

func desktopHub(pid int) (Hub, bool) {
	language, err := procinfo.Inspect(pid)
	if err != nil || language.PPID <= 1 {
		return Hub{}, false
	}
	path, err := procinfo.Path(pid)
	if err != nil || path != binary {
		return Hub{}, false
	}
	app, err := procinfo.Path(language.PPID)
	if err != nil || app != appExecutable {
		return Hub{}, false
	}
	hub := Hub{PID: pid, Start: language.Start}
	same, err := procinfo.SameProcess(language)
	return hub, err == nil && same
}
