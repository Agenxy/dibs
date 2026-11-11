// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestRegisterAndBindWarnAboutUnverifiedAppThread(t *testing.T) {
	testcodexipc.Start(t, "no-owner", nil)
	srv, _ := newServer(t)
	const thread = "6af44c23-af48-8191-b86e-c80e20c6802d"
	r := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "unverified-app", "nonce": "binding-advice-fixture", "session_id": "host-123",
		"harness": "Codex", "surface": "chatgpt-app",
	}, map[string]any{"threadId": thread, SurfaceMetaKey: "chatgpt-app"})
	token, _ := r["token"].(string)
	if token == "" || r["agent_id"] == nil {
		t.Fatalf("setup: registration was refused: %v", r)
	}
	assertUnverifiedBinding(t, r, thread)
	const replacement = "dc8fea77-2092-4c69-b810-c96a7bc5575d"
	r = toolCall(t, srv, "bind_session", map[string]any{"token": token, "session_id": replacement})
	if r["bound"] != true {
		t.Fatalf("setup: advisory refused a binding: %v", r)
	}
	assertUnverifiedBinding(t, r, replacement)
}

func assertUnverifiedBinding(t *testing.T, r map[string]any, thread string) {
	t.Helper()
	warning, _ := r["binding_warning"].(map[string]any)
	hint, _ := warning["hint"].(string)
	if warning["status"] != "unverified" || warning["thread_id"] != thread ||
		!strings.Contains(hint, "bind_session") {
		t.Errorf("successful binding hid unverified delivery or lost corrective call: %v", r)
	}
	assertNoNonceRecovery(t, hint)
}

func TestRegisterOwnedAppThreadNeedsNoBindingWarning(t *testing.T) {
	app := testcodexipc.Start(t, "idle", nil)
	srv, _ := newServer(t)
	r := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "owned-app", "harness": "Codex", "surface": "chatgpt-app",
	}, map[string]any{"threadId": testcodexipc.Thread, SurfaceMetaKey: "chatgpt-app"})
	if r["token"] == nil || r["binding_warning"] != nil {
		t.Fatalf("app ownership was not enough to verify registration: %v", r)
	}
	if len(app.Inputs()) != 0 {
		t.Fatal("binding check submitted input to the app")
	}
}

func TestRegisterLocalRolloutNeedsNoBindingWarning(t *testing.T) {
	testcodexipc.Start(t, "no-owner", nil)
	dir := filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal("setup:", err)
	}
	path := filepath.Join(dir, "rollout-fixture-"+testcodexipc.Thread+".jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\"}\n"), 0o600); err != nil {
		t.Fatal("setup:", err)
	}
	srv, _ := newServer(t)
	r := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "persisted-app", "harness": "Codex",
	}, map[string]any{"threadId": testcodexipc.Thread, SurfaceMetaKey: "chatgpt-app"})
	if r["token"] == nil || r["binding_warning"] != nil {
		t.Fatalf("local rollout was not enough to verify registration: %v", r)
	}
}

func TestRegisterCloudExecutionBindingNamesTheMeasuredCause(t *testing.T) {
	srv, _ := newServer(t)
	r := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "cloud-execution", "harness": "Codex", "cwd": t.TempDir(),
	}, map[string]any{"threadId": "6af44c23-af48-8191-b86e-c80e20c6802d", SurfaceMetaKey: "chatgpt-cloud-exec"})
	warning, _ := r["binding_warning"].(map[string]any)
	message, _ := warning["message"].(string)
	hint, _ := warning["hint"].(string)
	if r["token"] == nil || warning["status"] != "unsupported" ||
		!strings.Contains(message, "cloud-environment Codex thread") || !strings.Contains(hint, "local app thread") {
		t.Fatalf("measured cloud binding did not warn honestly with a local recovery step: %v", r)
	}
	assertNoNonceRecovery(t, hint)
}

func TestCloudSendAdvisoryIncludesNoNonceRecovery(t *testing.T) {
	testcodexipc.Start(t, "no-owner", nil)
	srv, eng, _ := newServerWithEngine(t)
	eng.SetWakePolicy(engine.WakeNone) // this guard reads the real send reply, never submits input
	worker := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "cloud-recovery", "harness": "Codex",
	}, map[string]any{"threadId": testcodexipc.Thread, SurfaceMetaKey: "chatgpt-cloud-exec"})
	if worker["token"] == nil || worker["agent_id"] == nil {
		t.Fatalf("setup worker: %v", worker)
	}
	sender := toolCall(t, srv, "register", map[string]any{"name": "recovery-sender"})
	if sender["token"] == nil {
		t.Fatalf("setup sender: %v", sender)
	}
	sent := toolCall(t, srv, "send", map[string]any{
		"token": sender["token"], "to": worker["agent_id"], "type": "notify", "body": "recovery fixture",
	})
	if sent["msg_serial"] == nil || sent["code"] != nil {
		t.Fatalf("setup send: %v", sent)
	}
	note, _ := sent["note"].(string)
	if !strings.Contains(note, "cloud-environment Codex thread") {
		t.Fatalf("send hid measured cloud cause: %v", sent)
	}
	assertNoNonceRecovery(t, note)
}

func assertNoNonceRecovery(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(text, "Without the nonce") ||
		!strings.Contains(text, `send(to:"coordinator", type:"request", adopt:"<old id>")`) {
		t.Errorf("recovery hint stranded an agent without its nonce: %s", text)
	}
}
