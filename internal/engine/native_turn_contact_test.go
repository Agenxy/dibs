// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// Send through the real native IPC client and record the resulting fixture
// turn IDs in the same bounded local rollout surface the daemon will read.
func TestNativeTurnFailuresHoldFurtherDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, code, errorText, want string
	}{
		{"context-full", "context_window_exceeded", "Error running remote compact task: fixture", "context is full"},
		{"compaction", "other", "Error running remote compact task: fixture", "reports a compaction failure"},
		{"other", "other", "private unknown failure details", "turns end immediately"},
		{"old-turn", "other", "private busy failure details", ""},
		{"different-id", "other", "private unrelated failure details", ""},
		{"auth-between-turns", "other", "private interrupted failure details", ""},
		{"wrong-thread", "other", "private unrelated thread details", ""},
		{"tool-called", "other", "private failure after a tool", ""},
		{"successful", "", "", ""},
		{"quiet-pair", "other", "private quiet failure", "turns end immediately"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			server := testcodexipc.Start(t, "sequence", nil)
			e, ctx, worker, sender := nativeAppEngine(t)
			e.SetHumanNotifier(noContactDesktop{})
			f := &acceptedContactFixture{e, ctx, server, worker, sender}
			path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions",
				"rollout-fixture-"+testcodexipc.Thread+"_019ffe52-0eaf-7f60-81cc-6ab1298d76ec.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			write := func(typ string, at time.Time, payload map[string]any) {
				t.Helper()
				if err := json.NewEncoder(file).Encode(map[string]any{
					"type": typ, "timestamp": at.UTC(), "payload": payload,
				}); err != nil {
					t.Fatal(err)
				}
			}
			header := testcodexipc.Thread
			if tc.name == "wrong-thread" {
				header = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
			}
			write("session_meta", time.Now(), map[string]any{"id": header})
			if tc.name == "compaction" {
				// A historical region larger than the read budget must not
				// prevent observing the current records in this continuation.
				if _, err := file.Seek(64<<20, 0); err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
			}
			var lastAccepted time.Time
			for n := 1; n <= 2; n++ {
				_, accepted := f.send(t, core.MsgRequest, n)
				lastAccepted = accepted
				id := fmt.Sprintf("turn-%d", n)
				start := accepted
				if tc.name == "old-turn" {
					start = start.Add(-time.Minute)
				}
				if tc.name == "different-id" {
					id = "unrelated-" + id
				}
				write("event_msg", start, map[string]any{"type": "task_started", "turn_id": id})
				if tc.name == "tool-called" {
					write("response_item", start, map[string]any{"type": "function_call", "name": "mcp__dibs__inbox"})
				}
				complete := map[string]any{"type": "task_complete", "turn_id": id, "duration_ms": 1}
				if tc.errorText != "" {
					complete["error"] = map[string]any{"message": tc.errorText, "codex_error_info": tc.code}
				}
				write("event_msg", start.Add(time.Millisecond), complete)
				if n == 1 && tc.name == "auth-between-turns" {
					if _, err := e.Inbox(ctx, worker); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.name == "quiet-pair" {
				f.sweep(t, lastAccepted.Add(3*time.Second))
				deadline := time.Now().Add(time.Second)
				for {
					notices, err := e.PendingForHuman(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if len(notices) == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("two failed turns without a later send never raised contact")
					}
					<-time.After(5 * time.Millisecond)
				}
			}
			third := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgRequest, Body: "private third request",
			})
			if third["msg_serial"] == nil {
				t.Fatalf("setup: third request not stored: %v", third)
			}
			waitWakeDone(t, e, "worker")
			if tc.want == "" {
				awaitNative(t, server, 3)
				return
			}
			if got := len(server.Inputs()); got != 2 {
				t.Fatalf("further native input was submitted after two failed turns: %d", got)
			}
			notices, err := e.PendingForHuman(ctx)
			if err != nil || len(notices) != 1 || notices[0].Contact == nil {
				t.Fatalf("two failed turns did not produce one contact alert: %v / %v", notices, err)
			}
			n := notices[0].Contact
			if !strings.Contains(n.OpenHint, tc.want) || n.OpenURL != "codex://threads/"+testcodexipc.Thread ||
				strings.Contains(n.OpenHint, tc.errorText) ||
				tc.name != "context-full" && strings.Contains(n.OpenHint, "context is full") {
				t.Fatalf("contact did not report only the proved reason and original thread: %+v", n)
			}
			led := e.led.(*memLedger)
			fenced, err := e.query(ctx, func() core.Result {
				for _, op := range led.ops {
					if op.Kind == "contact_escalated_v0.0.18" && op.AgentID == "worker" {
						return core.Result{"fenced": true}
					}
				}
				return nil
			})
			if err != nil || fenced["fenced"] != true {
				t.Fatalf("real diagnosis append lacked its reader boundary: %v / %v", fenced, err)
			}
			replayed, replayCtx := replayContactEngine(t, e, ctx, led)
			persisted, err := replayed.PendingForHuman(replayCtx)
			if err != nil || len(persisted) != 1 || persisted[0].Contact == nil ||
				!strings.Contains(persisted[0].Contact.OpenHint, tc.want) {
				t.Fatalf("replay lost the contact diagnosis: %v / %v", persisted, err)
			}
			if _, err := e.Inbox(ctx, worker); err != nil {
				t.Fatal(err)
			}
			f.send(t, core.MsgRequest, 3)
		})
	}
}
