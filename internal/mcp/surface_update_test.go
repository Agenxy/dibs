// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestUpdateSurfaceRequiresBridgeEvidenceAndMergesIdentity(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	reg := toolCall(t, srv, "register", map[string]any{
		"name": "surface-update", "description": "retained description", "nonce": "surface-update-nonce",
		"harness": "Codex", "model": "retained-model", "provider": "retained-provider",
		"title": "retained-title", "surface": harnessenv.ChatGPTApp,
	})
	if reg["token"] == nil {
		t.Fatalf("register setup: %v", reg)
	}
	info := func() *core.AgentInfo {
		t.Helper()
		board, err := eng.Board(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "surface-update" {
				if row["description"] != "retained description" {
					t.Fatalf("description replaced: %v", row)
				}
				return row["agent"].(*core.AgentInfo)
			}
		}
		t.Fatal("registered row missing")
		return nil
	}
	before := *info()
	if before.Surface != "" {
		t.Fatalf("model-only register app claim accepted: %q", before.Surface)
	}
	for _, tc := range []struct {
		name, want string
		meta       map[string]any
	}{
		{"model-only", "", nil},
		{"derived-terminal", harnessenv.CodexOutsideApp, map[string]any{SurfaceMetaKey: harnessenv.CodexOutsideApp}},
		{"derived-app", harnessenv.ChatGPTApp, map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolCallWithMeta(t, srv, "update", map[string]any{
				"token": reg["token"], "surface": harnessenv.ChatGPTApp,
			}, tc.meta)
			if got["ok"] != true {
				t.Fatalf("update refused: %v", got)
			}
			after := *info()
			if after.Surface != tc.want {
				t.Fatalf("surface=%q want %q", after.Surface, tc.want)
			}
			after.Surface = before.Surface
			if after != before {
				t.Fatalf("surface-only update replaced identity: before=%+v after=%+v", before, after)
			}
		})
	}
}
