// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestBridgeSurfaceUpdateDoesNotPretendTheModelCalled(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	reg := toolCall(t, srv, "register", map[string]any{
		"name": "surface-observer", "description": "retain me", "nonce": "surface-observer-nonce",
		"harness": "Codex", "model": "keep-model", "provider": "keep-provider", "title": "keep-title",
	})
	if reg["token"] == nil {
		t.Fatalf("setup register: %v", reg)
	}
	sender := toolCall(t, srv, "register", map[string]any{"name": "surface-sender", "description": "sender"})
	toolCall(t, srv, "check_in", map[string]any{"token": sender["token"]})
	sent, err := eng.Do(context.Background(), &core.Op{
		Kind: core.OpSendMessage, Token: sender["token"].(string), To: "surface-observer",
		MsgType: core.MsgQuestion, Body: "keep unread",
	})
	if err != nil || sent["msg_serial"] == nil {
		t.Fatalf("mail setup: %v %v", sent, err)
	}
	mailSnapshot := func() []byte {
		t.Helper()
		inbox, err := eng.ObserveInbox(context.Background(), reg["token"].(string))
		if err != nil {
			t.Fatal(err)
		}
		mail, ok := inbox["inbox"].([]*core.Message)
		if !ok || len(mail) != 1 || mail[0].State != core.MsgStatePending || mail[0].Consumed {
			t.Fatalf("mail was delivered or consumed by bridge evidence: %v", inbox)
		}
		raw, err := json.Marshal(mail)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	mailBefore := mailSnapshot()
	row := func() map[string]any {
		t.Helper()
		board, err := eng.Board(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range board["agents"].([]map[string]any) {
			if r["id"] == "surface-observer" {
				return r
			}
		}
		t.Fatal("setup row missing")
		return nil
	}
	before := row()
	identity := *before["agent"].(*core.AgentInfo)
	out := rpc(t, srv, "2026-07-28", "dibs/bridge-surface", map[string]any{
		"token": reg["token"], "_meta": map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp},
	})
	result, ok := out["result"].(map[string]any)
	if !ok || result["isError"] == true {
		t.Fatalf("surface evidence refused: %v", out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(result["content"].([]any)[0].(map[string]any)["text"].(string)), &res); err != nil {
		t.Fatal(err)
	}
	if res["ok"] != true {
		t.Fatalf("bridge surface update refused: %v", res)
	}
	after := row()
	if after["last_coordination_at"] != before["last_coordination_at"] || after["last_seen"] != before["last_seen"] {
		t.Fatalf("bridge surface fabricated model contact: coordination %v -> %v; seen %v -> %v",
			before["last_coordination_at"], after["last_coordination_at"], before["last_seen"], after["last_seen"])
	}
	got := *after["agent"].(*core.AgentInfo)
	if got.Surface != harnessenv.ChatGPTApp {
		t.Fatalf("late surface not applied: %+v", got)
	}
	got.Surface = identity.Surface
	if got != identity || after["description"] != before["description"] {
		t.Fatalf("bridge evidence replaced identity: %+v -> %+v", identity, got)
	}
	if !bytes.Equal(mailBefore, mailSnapshot()) {
		t.Fatal("surface completion altered unread mail")
	}
}

func TestBridgeSurfaceRPCIsStatelessAndCannotSelectAnotherAgent(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, server := newServerWithEngine(t)
			x := toolCall(t, srv, "register", map[string]any{"name": "surface-x", "description": "x"})
			y := toolCall(t, srv, "register", map[string]any{"name": "surface-y", "description": "y"})
			if x["token"] == nil || y["token"] == nil {
				t.Fatal("setup registration failed")
			}
			meta := map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp}
			before, err := eng.Board(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, params := range []map[string]any{
				{"token": y["token"], "agent_id": "surface-x", "_meta": meta},
				{"token": y["token"], "surface": harnessenv.ChatGPTApp},
				{"token": "invalid-token", "_meta": meta},
				{"token": y["token"], "_meta": map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp, HostMetaKey: "another-host"}},
				{"token": y["token"], "_meta": map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp, HostlessMetaKey: true}},
			} {
				out := rpc(t, srv, version, BridgeSurfaceEvidenceMethod, params)
				result, ok := out["result"].(map[string]any)
				if out["error"] == nil && (!ok || result["isError"] != true) {
					t.Fatalf("invalid/private authority accepted: %v", out)
				}
			}
			// The public listener supplies invitation authority through context,
			// never participant metadata. Enter through the real RPC dispatcher.
			invited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := engine.WithInvitation(r.Context(), engine.Invitation{Name: "surface-invitation"})
				server.ServeHTTP(w, r.WithContext(ctx))
			}))
			defer invited.Close()
			invitedReply := rpc(t, invited, version, BridgeSurfaceEvidenceMethod,
				map[string]any{"token": y["token"], "_meta": meta})
			invitedResult, _ := invitedReply["result"].(map[string]any)
			if invitedReply["error"] == nil && invitedResult["isError"] != true {
				t.Fatalf("invitation changed local bridge routing: %v", invitedReply)
			}
			afterRefusals, err := eng.Board(t.Context())
			if err != nil || afterRefusals["serial"] != before["serial"] {
				t.Fatalf("refused surface evidence changed replayable state: %v %v", afterRefusals, err)
			}
			out := rpc(t, srv, version, BridgeSurfaceEvidenceMethod, map[string]any{"token": y["token"], "_meta": meta})
			result, ok := out["result"].(map[string]any)
			if !ok || result["isError"] == true {
				t.Fatalf("own stateless evidence refused: %v", out)
			}
			board, err := eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			foundX, foundY := false, false
			for _, row := range board["agents"].([]map[string]any) {
				info := row["agent"].(*core.AgentInfo)
				if row["id"] == x["agent_id"] {
					foundX = true
					if info.Surface != "" {
						t.Fatal("another agent's token changed X's surface")
					}
				}
				if row["id"] == y["agent_id"] {
					foundY = true
					if info.Surface != harnessenv.ChatGPTApp {
						t.Fatal("token did not select only its own row")
					}
				}
			}
			if !foundX || !foundY {
				t.Fatalf("setup: authority check missed registered rows: X=%v Y=%v", foundX, foundY)
			}
			out = rpc(t, srv, version, "tools/call", map[string]any{
				"name": BridgeSurfaceEvidenceMethod, "arguments": map[string]any{"token": y["token"]},
			})
			toolResult, _ := out["result"].(map[string]any)
			if out["error"] == nil && toolResult["isError"] != true {
				t.Fatalf("private method reachable as a tool: %v", out)
			}
			listed, err := json.Marshal(rpc(t, srv, version, "tools/list", map[string]any{}))
			if err != nil || strings.Contains(string(listed), BridgeSurfaceEvidenceMethod) {
				t.Fatalf("private method advertised as a tool: %v", err)
			}
		})
	}
}
