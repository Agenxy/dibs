// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"log/slog"
	"os"
	"sync"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mcp"
)

var (
	antigravityStdio = sync.OnceValue(func() bool {
		_, verified := antigravity.Bridge(os.Getpid())
		return verified
	})
	antigravityObservations = make(map[string]bool)
)

func enrichAntigravity(meta map[string]any) {
	delete(meta, antigravity.BridgeMeta)
	if !antigravityStdio() {
		return
	}
	markAntigravity(meta)
	conversation := antigravityConversation(meta)
	if !antigravityObservations[conversation] && len(antigravityObservations) < 1024 {
		antigravityObservations[conversation] = true
		slog.Info("Antigravity app MCP conversation metadata", "present", conversation != "")
	}
}

// One MCP child serves several conversations. The daemon derives the session
// from the original harness metadata after verifying the child itself.
func markAntigravity(meta map[string]any) {
	meta[antigravity.BridgeMeta] = true
	meta[mcp.SessionMetaKey] = ""
	delete(meta, "threadId")
	delete(meta, mcp.SurfaceMetaKey)
}

func antigravityConversation(meta map[string]any) string {
	id, _ := meta[antigravity.ConversationMeta].(string)
	if core.LooksLikeThreadID(id) {
		return id
	}
	return ""
}

func antigravityBridgeMarked(params map[string]any) bool {
	meta, _ := params["_meta"].(map[string]any)
	marked, _ := meta[antigravity.BridgeMeta].(bool)
	return marked
}
