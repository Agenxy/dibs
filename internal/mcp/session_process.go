// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/localtransport"
)

func (s *Server) observeSessionProcess(ctx context.Context, params json.RawMessage, token string) context.Context {
	peer := localtransport.PeerPID(ctx)
	if token == "" || peer <= 1 {
		return ctx
	}
	if _, invited := engine.InvitationFrom(ctx); invited {
		return ctx
	}
	pid, start := bridgeProcessMetadata(params)
	if pid != peer || metaHostless(params) || !slices.Contains(s.eng.SelfHostIDs(), resolveHostID(ctx, params)) {
		return ctx
	}
	if process, ok := harnessenv.ClaudeProcessForBridge(peer, start, metaSession(params)); ok {
		return engine.WithClaudeProcess(ctx, process)
	}
	return ctx
}
