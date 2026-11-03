// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// Legacy bindings have no surface. Their host can infer ChatGPT from its
// local rollout header, so the hub must not hand out an open-capable plan.
// Enter the production plan/execution doors after storing actual mail; this
// isolates navigation policy from the ordinary peer-cache admission worker.
func TestArchivedLegacyRemoteAppPlansCannotReachAnOldOpeningBridge(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "native-only"}[capable], func(t *testing.T) {
			e := New(core.NewState("hub", core.DefaultLimits()), &memLedger{}, deadProber{})
			e.SetWakePolicy(WakeNone)
			caps := []bool{true}
			if capable {
				caps = append(caps, true)
			}
			requests, detach, err := e.AttachHostBridgeCapabilities("remote", []string{"codex"}, nil, 0, caps...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(detach)
			ctx, cancel := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() { e.Run(ctx); close(joined) }()
			t.Cleanup(func() { cancel(); <-joined })
			stopWakeTimersOnCleanup(t, e)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil || r["error"] != nil {
					t.Fatalf("setup: %v %v", r, err)
				}
				return r
			}
			do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: testcodexipc.Thread, Agent: &core.AgentInfo{Harness: "Codex", HostID: "remote"}})
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			_, err = e.query(ctx, func() core.Result { e.state.Agents["worker"].Status = core.StatusArchived; return nil })
			if err != nil {
				t.Fatal(err)
			}
			do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "retained legacy mail"})
			var plan wakePlan
			setup, err := e.query(ctx, func() core.Result {
				var ok bool
				plan, ok = e.wakeFor(e.state.Agents["worker"], core.MsgQuestion, core.Event{Type: "message.sent", Agent: "sender", To: "worker", Data: map[string]any{"msg_type": core.MsgQuestion}})
				return core.Result{"made": ok, "host": plan.host, "thread": plan.thread, "keys": len(e.deliveryKeysAt("worker", time.Now()))}
			})
			if err != nil || setup["made"] != true || setup["host"] != "remote" || setup["thread"] != testcodexipc.Thread || setup["keys"] == 0 {
				t.Fatalf("setup did not make a real remote plan: %v %v", setup, err)
			}
			done := make(chan bool, 1)
			go func() { defer e.wakeExited("worker", plan.thread); done <- e.runWake(plan, "worker") }()
			select {
			case request := <-requests:
				if !e.ReportWakeResult(WakeResult{ID: request.ID, Host: "remote", OK: true, NativeDelivery: "accepted"}) {
					t.Fatal("setup: report refused")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("reported request did not settle")
				}
				waitWakeDone(t, e, "worker")
				if !capable {
					t.Fatal("legacy archived plan reached an older bridge that can infer an app and navigate")
				}
				raw, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				if wire["no_navigation"] != true {
					t.Fatalf("legacy archived plan permits navigation: %s", raw)
				}
			case <-done:
				waitWakeDone(t, e, "worker")
				if capable {
					t.Fatal("capable bridge received no legacy native-only request")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("legacy remote decision did not settle")
			}
		})
	}
}
