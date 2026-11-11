// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestObserveCodexTurnFailureEvidence(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	for _, tc := range []struct {
		name, code, message string
		duration            int64
		tool, unfinished    bool
		want                CodexTurnObservation
	}{
		{name: "compact-18s", code: "context_window_exceeded", message: "Error running remote compact task: fixture", duration: 18000, want: CodexTurnObservation{Failure: core.ContactContextFull}},
		{name: "compact-37s", code: "other", message: "Error running remote compact task: fixture", duration: 37000, want: CodexTurnObservation{Failure: core.ContactCompactionFailed}},
		{name: "context-code-only", code: "context_window_exceeded", message: "context exhausted", duration: 37000, want: CodexTurnObservation{Failure: core.ContactContextFull}},
		{name: "named-without-duration", code: "other", message: "Error running remote compact task: fixture", want: CodexTurnObservation{Failure: core.ContactCompactionFailed}},
		{name: "generic-at-cap", code: "other", message: "unrecognized failure", duration: 2000, want: CodexTurnObservation{Failure: core.ContactTurnsEnded}},
		{name: "generic-above-cap", code: "other", message: "unrecognized failure", duration: 2001, want: CodexTurnObservation{Normal: true}},
		{name: "generic-37s", code: "other", message: "unrecognized failure", duration: 37000, want: CodexTurnObservation{Normal: true}},
		{name: "generic-without-duration", code: "other", message: "unrecognized failure"},
		{name: "tool-before-compact", code: "context_window_exceeded", message: "Error running remote compact task: fixture", duration: 37000, tool: true, want: CodexTurnObservation{Normal: true}},
		{name: "successful", duration: 37000, want: CodexTurnObservation{Normal: true}},
		{name: "unfinished-compact", unfinished: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			dir := filepath.Join(home, "sessions")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(filepath.Join(dir, "rollout-fixture-"+thread+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			write := func(kind string, at time.Time, payload map[string]any) {
				t.Helper()
				if err := json.NewEncoder(file).Encode(map[string]any{
					"type": kind, "timestamp": at, "payload": payload,
				}); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now().Add(-time.Minute)
			write("session_meta", start, map[string]any{"id": thread})
			write("event_msg", start, map[string]any{"type": "task_started", "turn_id": "accepted"})
			if tc.tool {
				write("response_item", start, map[string]any{"type": "function_call", "name": "fixture_tool"})
			}
			if !tc.unfinished {
				done := map[string]any{"type": "task_complete", "turn_id": "accepted", "duration_ms": tc.duration}
				if tc.message != "" {
					done["error"] = map[string]any{"message": tc.message, "codex_error_info": tc.code}
				}
				write("event_msg", start.Add(time.Duration(tc.duration)*time.Millisecond), done)
			}
			if !tc.unfinished {
				// An unreceipted app/person turn must never supply a diagnosis.
				write("event_msg", start.Add(40*time.Second), map[string]any{"type": "task_started", "turn_id": "unrelated"})
				write("event_msg", start.Add(41*time.Second), map[string]any{
					"type": "task_complete", "turn_id": "unrelated", "duration_ms": 1000,
					"error": map[string]any{"message": "Error running remote compact task: unrelated", "codex_error_info": "context_window_exceeded"},
				})
			}
			got := ObserveCodexTurns(thread, [2]CodexTurnReceipt{{ID: "accepted", OfferedAt: start}})[0]
			if got != tc.want {
				t.Fatalf("receipt-matched observation: got %+v, want %+v", got, tc.want)
			}
		})
	}
}
