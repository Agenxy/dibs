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

func TestChangedRemoteSurfaceDeliversOnlyThroughCurrentBinding(t *testing.T) {
	for _, tc := range []struct {
		name              string
		changed, archived bool
	}{{"unchanged-active", false, false}, {"changed-active", true, false}, {"unchanged-archived", false, true}, {"changed-archived", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			limits := core.DefaultLimits()
			limits.StaleGrace = time.Nanosecond
			e := New(core.NewState("hub", limits), &memLedger{}, deadProber{})
			e.SetWakePolicy(WakeNone)
			requests, detach, err := e.AttachHostBridgeCapabilities("remote", []string{"codex"}, nil, 0, true)
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
			surface := "codex"
			if tc.changed {
				surface = "chatgpt-app"
			}
			worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "surface-fixture", AgentKind: core.KindEphemeral, SessionID: testcodexipc.Thread, Agent: &core.AgentInfo{Harness: "Codex", HostID: "remote", Surface: surface}})["token"].(string)
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			mail := do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "real pending mail"})["msg_serial"].(uint64)
			var plan wakePlan
			setup, err := e.query(ctx, func() core.Result {
				var ok bool
				plan, ok = e.wakeFor(e.state.Agents["worker"], core.MsgQuestion, core.Event{Type: "message.sent", Agent: "sender", To: "worker"})
				return core.Result{"made": ok}
			})
			if err != nil || setup["made"] != true || plan.host != "remote" || plan.request.Surface != surface {
				t.Fatalf("setup plan: %+v %v %v", plan.request, setup, err)
			}
			if tc.changed {
				do(&core.Op{Kind: core.OpUpdate, Token: worker, Agent: &core.AgentInfo{Surface: "codex"}})
			}
			if tc.archived {
				do(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}, KeepArchivedNonce: true})
				do(&core.Op{Kind: core.OpSweep, KeepArchivedNonce: true})
			}
			state, err := e.query(ctx, func() core.Result {
				l := e.state.Agents["worker"]
				return core.Result{"archived": l.Status == core.StatusArchived, "same": l.CreatedSerial == plan.createdSerial && threadIDOf(l) == plan.thread, "surface": surfaceOf(l), "host": e.remoteHostOf(l)}
			})
			if err != nil || state["archived"] != tc.archived || state["same"] != true || state["surface"] != "codex" || state["host"] != "remote" {
				t.Fatalf("setup transition: %v %v", state, err)
			}
			done := make(chan bool, 1)
			go func() { defer e.wakeExited("worker", plan.thread); done <- e.runWake(plan, "worker") }()
			select {
			case request := <-requests:
				if request.Surface != "codex" || request.NoNavigation {
					if !e.ReportWakeResult(WakeResult{ID: request.ID, Host: "remote", OK: true}) {
						t.Fatal("setup stale request report refused")
					}
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("stale handed-off request did not settle")
					}
					t.Fatalf("stale app route reached opening bridge: surface=%s no_navigation=%v", request.Surface, request.NoNavigation)
				}
				if tc.archived {
					worker = do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "surface-fixture", AgentKind: core.KindEphemeral, SessionID: testcodexipc.Thread, Agent: &core.AgentInfo{Harness: "Codex", HostID: "remote", Surface: "codex"}})["token"].(string)
				}
				inbox, err := e.InboxPage(ctx, worker, "", 8)
				if err != nil || inbox["error"] != nil {
					t.Fatalf("recipient could not read after current route wake: %v %v", inbox, err)
				}
				raw, err := json.Marshal(inbox)
				if err != nil {
					t.Fatal(err)
				}
				var page struct {
					Messages []struct {
						Serial uint64
						Body   string
					}
				}
				if err = json.Unmarshal(raw, &page); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, m := range page.Messages {
					if m.Serial == mail && m.Body == "real pending mail" {
						found = true
					}
				}
				if !found {
					t.Fatalf("current route did not expose original mail: %s", raw)
				}
				if !e.ReportWakeResult(WakeResult{ID: request.ID, Host: "remote", OK: true}) {
					t.Fatal("setup report refused")
				}
				select {
				case ok := <-done:
					if !ok {
						t.Fatal("fresh route was not accepted")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("request did not settle")
				}
				select {
				case next := <-requests:
					t.Fatalf("unexpected second handed-off request: %+v", next)
				default:
				}
			case <-done:
				t.Fatal("changed route dropped mail instead of replanning once")
			case <-time.After(3 * time.Second):
				t.Fatal("decision did not settle")
			}
		})
	}
}
