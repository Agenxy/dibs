// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// BridgeSurfaceEvidenceMetaKey advertises the optional private RPC. The
// method name itself is the wire fence: an older daemon cannot mistake this
// evidence for an ordinary update, even after an address or image change.
const BridgeSurfaceEvidenceMetaKey = "com.dibs/bridge_surface_evidence"

// BridgeSurfaceEvidenceMethod is not a tool and has no model-facing schema.
const BridgeSurfaceEvidenceMethod = "dibs/bridge-surface"

// Only an own-host registration reply on the local listener can arm the
// bridge's optional completion. Other tools and invited/remote callers cannot
// use that RPC, so advertising it there would promise an unavailable route.
func (s *Server) surfaceRegistrationResult(ctx context.Context, name string, params json.RawMessage, text string) any {
	result := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	local, _ := ctx.Value(hostIDKey{}).(string)
	_, invited := engine.InvitationFrom(ctx)
	if (name == "register" || name == "resume") && local != "" && !invited &&
		!metaHostless(params) && resolveHostID(ctx, params) == s.eng.HostID() {
		result["_meta"] = map[string]any{BridgeSurfaceEvidenceMetaKey: true}
	}
	return result
}

func (s *Server) callBridgeSurfaceEvidence(ctx context.Context, params json.RawMessage) any {
	refusal := func() any {
		return sendTextResult(core.Result{
			"code": "E_BAD_BRIDGE_SURFACE", "message": "bridge evidence requires a local surface-only update",
			"hint": "let the local stdio bridge report its own surface; use ordinary update for model-authored changes",
		}, true)
	}
	var p struct {
		Token string `json:"token"`
	}
	var fields map[string]json.RawMessage
	local, _ := ctx.Value(hostIDKey{}).(string)
	_, invited := engine.InvitationFrom(ctx)
	if json.Unmarshal(params, &p) != nil || json.Unmarshal(params, &fields) != nil || len(fields) != 2 ||
		fields["token"] == nil || fields["_meta"] == nil ||
		local == "" || resolveHostID(ctx, params) != s.eng.HostID() || metaHostless(params) || invited {
		return refusal()
	}
	surface := resolveSurface(params, "")
	if surface != harnessenv.ChatGPTApp && surface != harnessenv.ClaudeDesktop && surface != harnessenv.CodexOutsideApp {
		return refusal()
	}
	op := &core.Op{
		Kind: core.OpUpdate, Token: p.Token, Agent: &core.AgentInfo{Surface: surface}, KeepDescription: true,
		BridgeSurfaceEvidence: true, BridgeSurfaceTrusted: true,
	}
	res, err := s.eng.Do(ctx, op)
	if err != nil {
		return sendTextResult(core.Result{
			"code": "E_BRIDGE_SURFACE", "message": err.Error(),
			"hint": "a later authenticated request can supply current bridge evidence",
		}, true)
	}
	return sendTextResult(res, false)
}
