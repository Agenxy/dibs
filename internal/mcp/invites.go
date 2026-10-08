// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
)

type inviteBindingKey struct{}

// SetInvites attaches the private listener's policy-driven issuance service.
func (s *Server) SetInvites(service *invites.Service) { s.invites = service }

func (s *Server) issueInvite(ctx context.Context, a *toolArgs) (core.Result, error) {
	if s.invites == nil {
		return nil, cloudRefusal("invitation service not configured", "ask the operator to configure a public listener")
	}
	return s.invites.Handle(ctx, a.Token, a.InviteAction, a.Name, a.IssuedBy, a.InviteTTLS, a.InviteExport)
}

// WithInviteBinding is called only by the public listener after verifying an
// invitation. Binding persistence runs outside the writer loop.
func WithInviteBinding(ctx context.Context, bind func(string) error) context.Context {
	return context.WithValue(ctx, inviteBindingKey{}, bind)
}

func cloudRefusal(why, hint string) *core.Error {
	return &core.Error{Code: "E_INVITE_SCOPE", Msg: why, Hint: hint}
}

// Invited clients are pull-only, not local harness integrations. The allowlist
// keeps new hook/admin methods closed until their invite boundary is reviewed.
func inviteToolAllowed(name string) bool {
	switch name {
	case "register", "resume", "check_in", "update", "heartbeat", "sign_off",
		"declare", "undeclare", "send", "put_blob", "get_blob", "upload", "download", "read_mail", "respond",
		"ack", "inbox", "claim", "release", "events_since", "await_events", "board",
		"open_space", "join_space", "leave_space", "read_space", "post", "watch_space", "ack_announcement":
		return true
	}
	return false
}

func prepareInvitedTool(ctx context.Context, name string, a *toolArgs) (context.Context, error) {
	i, invited := engine.InvitationFrom(ctx)
	if !invited {
		return ctx, nil
	}
	if !inviteToolAllowed(name) {
		return ctx, cloudRefusal("that tool is not available through an invitation",
			"use agent-scoped coordination tools; ask a local coordinator or the human for staff actions")
	}
	if name == "register" && a.Name != i.Name {
		return ctx, cloudRefusal("register must use the invitation's exact name",
			"register as "+i.Name+" with a retained random nonce")
	}
	if name == "update" && a.Name != "" && a.Name != i.Name {
		return ctx, cloudRefusal("invited identities cannot be renamed",
			"keep the invitation's original name; ask the operator for a new identity")
	}
	if name == "put_blob" && a.Path != "" {
		return ctx, cloudRefusal("an invite cannot read a file on the board's machine",
			"send your own bytes as base64 in put_blob.data, not a path")
	}
	if name == "get_blob" {
		if a.As == "path" {
			return ctx, cloudRefusal("a cloud client cannot open a hub-local materialized file", "use get_blob with as: inline")
		}
		a.As = "inline"
	}
	if suppliedHarnessIdentity(a) {
		return ctx, cloudRefusal("an invite does not claim harness sessions or local processes",
			"omit session_id, parent, parent_nonce and pid; recover using your nonce")
	}
	return engine.WithInvitationToken(ctx, a.Token), nil
}

func suppliedHarnessIdentity(a *toolArgs) bool {
	return a.SessionID != "" || a.Parent != "" || a.ParentNonce != "" || a.PID != 0
}

// Project the authenticated listener's actual capabilities at the server,
// never in a guest bridge. Invitations remain pull-only on both protocol eras.
// A different authorization context cannot share a discovery response.
func invitedDiscovery(ctx context.Context, result map[string]any) map[string]any {
	if _, invited := engine.InvitationFrom(ctx); !invited {
		return result
	}
	caps, _ := result["capabilities"].(map[string]any)
	caps["tools"] = map[string]any{}
	caps["resources"] = map[string]any{}
	if _, modern := result["cacheScope"]; modern {
		result["cacheScope"] = scopePrivate
	}
	result["instructions"] = "Dibs invitation access is pull-only. Register with the invitation's exact name and " +
		"retained nonce, never pid, session_id or parent identity. Keep the agent token; call check_in and inbox " +
		"at each activation. Tasks are polled; no subscriptions, local-board secret or wake route is available."
	return result
}

func bindInvitedAgent(ctx context.Context, name string, res core.Result) error {
	if name != "register" {
		return nil
	}
	if bind, ok := ctx.Value(inviteBindingKey{}).(func(string) error); ok {
		id, _ := res["agent_id"].(string)
		if err := bind(id); err != nil {
			return cloudRefusal("invite binding could not be persisted",
				"retry register with the same name and nonce; if the invite changed, ask the operator for a current key")
		}
	}
	return nil
}

// prepareInvitedRequest covers resources and task methods as well as tools.
// There is no streaming wake capability on a public invitation connection.
func prepareInvitedRequest(ctx context.Context, req *rpcRequest) (context.Context, *rpcError) {
	if _, ok := engine.InvitationFrom(ctx); !ok {
		return ctx, nil
	}
	var p struct {
		URI  string         `json:"uri"`
		Meta map[string]any `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params, &p)
	allowed := false
	switch req.Method {
	case "server/discover", "initialize", "ping", "tools/list", "tools/call",
		"notifications/initialized", "notifications/cancelled",
		"resources/list", "resources/templates/list", "resources/read", "prompts/list", "prompts/get",
		"tasks/get", "tasks/update", "tasks/cancel":
		allowed = true
	}
	if !allowed || strings.HasPrefix(p.URI, "dibs://wake") || p.URI == "dibs://self-wake" {
		return ctx, &rpcError{
			Code:    -32602,
			Message: "invited cloud agents are pull-only; wake, hook and subscription routes are unavailable",
			Data:    hint("call inbox/check_in or await_events with your own agent token"),
		}
	}
	token, _ := p.Meta[metaTokenKey].(string)
	return engine.WithInvitationToken(ctx, token), nil
}
