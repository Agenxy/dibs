// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestWakeDiagnosticsReadsAppMetadataThroughMCP(t *testing.T) {
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	const notice = "Dibs: question notice issued at 2026-11-20T18:00:00Z. It may already be handled."
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: build app fixture: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1")
	t.Setenv("DIBS_TEST_QUEUE_THREAD_SOURCE", "appServer")
	previous := harnessenv.RealShower
	harnessenv.RealShower = harnessenv.Shower{
		Holds: func(string) bool { return true },
		Open:  func([]string) error { return nil },
	}
	t.Cleanup(func() { harnessenv.RealShower = previous })
	srv, eng, _ := newServerWithEngine(t)
	argv := []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}
	eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": {Argv: argv, Cooldown: time.Millisecond}})
	worker := registerObservationAppWorker(t, srv, eng, thread, home)
	lead := toolCall(t, srv, "register", map[string]any{"name": "wake-lead", "nonce": "wake-diagnostics-lead"})
	if lead["token"] == nil || lead["code"] != nil {
		t.Fatalf("setup: register coordinator: %v", lead)
	}
	if _, err := eng.GrantRole(context.Background(), "wake-lead", core.RoleCoordinator); err != nil {
		t.Fatal("setup: grant coordinator role:", err)
	}
	writeObservationQueue(t, home, thread, notice)
	queuePath := filepath.Join(home, "pending.json")
	before, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatal("setup: read app queue fixture:", err)
	}
	_, serial, err := eng.SubscribeInfo(context.Background(), "")
	if err != nil {
		t.Fatal("setup: read board serial:", err)
	}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			refused := toolCallOn(t, srv, version, "diagnose_wake", map[string]any{
				"token": worker["token"], "agent": "queue-worker",
			})
			if refused["code"] != "E_NOT_COORDINATOR" {
				t.Fatalf("diagnose_wake must refuse unprivileged lookup: %v", refused)
			}
			got := toolCallOn(t, srv, version, "diagnose_wake", map[string]any{
				"token": lead["token"], "agent": "queue-worker",
			})
			if got["thread"] != thread || got["serial"] != float64(serial) {
				t.Fatalf("diagnostics lost binding or changed the board: %v", got)
			}
			app, _ := got["app"].(map[string]any)
			if app["thread_source"] != "appServer" || app["pending_count"] != float64(1) ||
				app["dibs_pending_count"] != float64(1) || app["oldest_dibs_notice_at"] != "2026-11-20T18:00:00Z" ||
				app["last_turn_started_at"] != time.Unix(1795200000, 0).UTC().Format(time.RFC3339) ||
				app["native_ipc"] != "no_socket" || app["app_owns_thread"] != nil {
				t.Fatalf("diagnostics lost app evidence or invented native ownership: %v", got)
			}
			payload, err := json.Marshal(got)
			if err != nil || bytes.Contains(payload, []byte(notice)) || bytes.Contains(payload, []byte("fixture-existing")) {
				t.Fatalf("diagnostics returned queue content or item identity: %s (error %v)", payload, err)
			}
		})
	}
	after, err := os.ReadFile(queuePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("diagnostics changed the app queue: %v", err)
	}
	_, afterSerial, err := eng.SubscribeInfo(context.Background(), "")
	if err != nil || afterSerial != serial {
		t.Fatalf("diagnostics changed board serial %d to %d: %v", serial, afterSerial, err)
	}
	assertWakeDiagnosticsRecordsActualAttempt(t, srv, eng, lead["token"])
}

func assertWakeDiagnosticsRecordsActualAttempt(
	t *testing.T, srv *httptest.Server, eng *engine.Engine, token any,
) {
	t.Helper()
	if _, err := eng.Do(t.Context(), &core.Op{Kind: core.OpSweep, DeadAgents: []string{"queue-worker"}}); err != nil {
		t.Fatal("setup: set worker dormant:", err)
	}
	sent := toolCall(t, srv, "send", map[string]any{
		"token": token, "to": "queue-worker", "type": "question", "body": "private diagnostic mail",
	})
	if sent["msg_serial"] == nil || sent["code"] != nil {
		t.Fatalf("setup: send to dormant worker: %v", sent)
	}
	until := time.Now().Add(5 * time.Second)
	for {
		got := toolCall(t, srv, "diagnose_wake", map[string]any{"token": token, "agent": "queue-worker"})
		if attempt, ok := got["last_attempt"].(map[string]any); ok {
			if attempt["route"] != "app_queue" || attempt["outcome"] != "retained" || attempt["observed_at"] == nil {
				t.Fatalf("diagnostics lost the actual queue outcome: %v", got)
			}
			return
		}
		if time.Now().After(until) {
			t.Fatalf("diagnostics never observed the queue route: %v", got)
		}
		<-time.After(150 * time.Millisecond)
	}
}
