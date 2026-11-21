// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"testing"

	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestAppThreadSurvivesPlaceholderOnlyCheckIn(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	const thread = "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b"
	const nonce = "fixture-app-thread-credential"
	body := map[string]any{"name": "app-worker", "nonce": nonce, "session_id": "host-2256", "harness": "Codex"}
	app := map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp, "threadId": thread}
	reg := toolCallWithMeta(t, srv, "register", body, app)
	token := reg["token"]
	if token == nil || reg["code"] != nil {
		t.Fatalf("setup: register app agent: %v", reg)
	}
	current := func(wantSession, wantAppThread string) {
		t.Helper()
		board, err := eng.Board(t.Context())
		if err != nil {
			t.Fatal("read board:", err)
		}
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] != reg["agent_id"] {
				continue
			}
			sessions := row["sessions"].(map[string]any)
			if sessions["current"] != wantSession || sessions["app_thread"] != wantAppThread {
				t.Fatalf("wrong current session or app wake target: %v", sessions)
			}
			return
		}
		t.Fatal("setup: app agent is missing")
	}
	current(thread, thread)
	checkIn := func(placeholder string) {
		t.Helper()
		res := toolCallWithMeta(t, srv, "check_in", map[string]any{"token": token},
			map[string]any{SessionMetaKey: placeholder})
		if res["ok"] != true {
			t.Fatalf("setup: check_in failed: %v", res)
		}
	}
	checkIn("host-25783")
	current(thread, thread)
	checkIn("host-69125")
	current(thread, thread)

	// Existing rows can already be displaced. A call that actually names
	// the app thread repairs them; the repair never scans the old aliases.
	displaced := toolCall(t, srv, "bind_session", map[string]any{"token": token, "session_id": "host-69125"})
	if displaced["bound"] != true {
		t.Fatalf("setup: could not reproduce an already-displaced binding: %v", displaced)
	}
	current("host-69125", "")
	repaired := toolCallWithMeta(t, srv, "check_in", map[string]any{"token": token}, app)
	if repaired["ok"] != true {
		t.Fatalf("check-in carrying the app thread was refused: %v", repaired)
	}
	current(thread, thread)

	// A real nonce recovery that states a new non-thread activation still
	// leaves its retired app alias behind, even if it states no new surface.
	body["session_id"] = "host-88201"
	moved := toolCallWithMeta(t, srv, "register", body, map[string]any{SessionMetaKey: "host-88201"})
	token = moved["token"]
	if token == nil || token == reg["token"] || moved["agent_id"] != reg["agent_id"] {
		t.Fatalf("setup: nonce recovery did not move the same identity: %v", moved)
	}
	current("host-88201", "")
	checkIn("host-89913")
	current("host-89913", "")
}
