// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/localtransport"
)

type antigravityCallKey struct{}

type antigravityCall struct {
	hub          antigravity.Hub
	conversation string
}

var verifyAntigravityBridge = antigravity.Bridge

func (s *Server) antigravityCall(ctx context.Context, params json.RawMessage) *antigravityCall {
	peer := localtransport.PeerPID(ctx)
	if peer <= 1 {
		return nil
	}
	if _, invited := engine.InvitationFrom(ctx); invited {
		return nil
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil || string(p.Meta[antigravity.BridgeMeta]) != "true" ||
		metaHostless(params) || !slices.Contains(s.eng.SelfHostIDs(), resolveHostID(ctx, params)) {
		return nil
	}
	hub, ok := verifyAntigravityBridge(peer)
	if !ok {
		return nil
	}
	var conversation string
	_ = json.Unmarshal(p.Meta[antigravity.ConversationMeta], &conversation)
	if !core.LooksLikeThreadID(conversation) {
		conversation = ""
	}
	return &antigravityCall{hub: hub, conversation: conversation}
}

func (s *Server) prepareSessionForTool(ctx context.Context, token string, params json.RawMessage) context.Context {
	if app := s.antigravityCall(ctx, params); app != nil {
		return context.WithValue(ctx, antigravityCallKey{}, app)
	}
	if _, invited := engine.InvitationFrom(ctx); invited {
		return ctx
	}
	// Attach before the tool, so check_in can return the current session.
	s.adoptSession(ctx, token, params)
	return s.observeSessionProcess(ctx, params, token)
}

func (s *Server) bindAntigravityCall(ctx context.Context, tool, token string, res core.Result) {
	app, _ := ctx.Value(antigravityCallKey{}).(*antigravityCall)
	if app == nil {
		return
	}
	switch tool {
	case "register", "resume":
		token, _ = res["token"].(string)
	case "check_in":
	default:
		return
	}
	if token != "" {
		s.eng.BindAntigravity(ctx, token, app.hub, app.conversation)
	}
}

func (s *Server) prepareAgentRegistration(
	ctx context.Context, params json.RawMessage, a *toolArgs, client *clientInfoJSON, op *core.Op,
) {
	op.Agent = agentInfo(ctx, params, a, client)
	if app, _ := ctx.Value(antigravityCallKey{}).(*antigravityCall); app != nil {
		op.Agent.Harness = antigravity.Harness
		op.Agent.Surface = ""
		op.NoProcess = true
	}
}
