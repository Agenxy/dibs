// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Send and update run in one writer turn. The native remote route plans the
// wake synchronously during publish, but its worker cannot enter the writer
// until after the update. This drives production execution and reporting,
// rather than assigning a no-navigation flag or a replan budget by hand.
func TestRemoteRouteChangeThroughProductionWake(t *testing.T) {
	for _, scenario := range []string{"surface", "host", "thread", "local-socket", "missing-route", "handled-missing-route", "archived-current"} {
		t.Run(scenario, func(t *testing.T) {
			sock, session := listeningSession(t)
			limits := core.DefaultLimits()
			limits.StaleGrace = time.Nanosecond
			e := New(core.NewState("hub", limits), &memLedger{}, deadProber{})
			e.SetWakePolicy(WakeNone)
			old, detach, err := e.AttachHostBridgeCapabilities("remote", []string{"codex"}, nil, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(detach)
			current, detachCurrent, err := e.AttachHostBridgeCapabilities("current", []string{"codex"}, nil, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(detachCurrent)
			ctx, cancel := context.WithCancel(context.Background())
			go e.Run(ctx)
			t.Cleanup(func() { cancel(); <-e.Done() })
			stopWakeTimersOnCleanup(t, e)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil || r["error"] != nil {
					t.Fatalf("setup: %v %v", r, err)
				}
				return r
			}
			worker := do(&core.Op{
				Kind: core.OpRegister, Name: "worker", Nonce: "route-production", AgentKind: core.KindEphemeral, SessionID: session,
				Agent: &core.AgentInfo{Harness: "Codex", HostID: "remote", Surface: harnessenv.ChatGPTApp, CWD: "/w"},
			})["token"].(string)
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			var serial uint64
			var capturedCreated uint64
			setup, err := e.query(ctx, func() core.Result {
				e.primePeerSessions()
				e.SetWakePolicy(WakeAll)
				r, err := e.exec(&core.Op{
					Kind: core.OpSendMessage, Token: sender, To: "worker",
					MsgType: core.MsgQuestion, Body: "pending-route-marker",
				}, time.Now())
				if err != nil || r["error"] != nil {
					return core.Result{"setup_error": r, "err": err}
				}
				serial, _ = r["msg_serial"].(uint64)
				capturedCreated = e.state.Agents["worker"].CreatedSerial
				e.wakers.mu.Lock()
				running := e.wakers.running["worker"]
				e.wakers.mu.Unlock()
				if !running {
					return core.Result{"setup_error": "send did not reserve a native worker"}
				}
				info := &core.AgentInfo{Surface: "codex"}
				switch scenario {
				case "host":
					info.HostID = "current"
				case "archived-current":
					info.HostID, info.Surface = "current", harnessenv.ChatGPTApp
				case "local-socket":
					info.HostID, info.Harness, info.Surface = "hub", "Claude Code", "cli"
				case "missing-route", "handled-missing-route":
					info.HostID = "unattached"
					if scenario == "handled-missing-route" {
						r, err = e.exec(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: serial, Disposition: "answer", Body: "already handled"}, time.Now())
						if err != nil || r["error"] != nil {
							return core.Result{"setup_error": r, "err": err}
						}
					}
				}
				update := &core.Op{Kind: core.OpUpdate, Token: worker, Agent: info}
				wantThread := session
				if scenario == "thread" {
					wantThread = remoteThread
					update = &core.Op{
						Kind: core.OpRegister, Name: "worker", Nonce: "route-production", SessionID: wantThread,
						Agent: &core.AgentInfo{Harness: "Codex", Surface: "codex", HostID: "remote", CWD: "/w"},
					}
				}
				r, err = e.exec(update, time.Now())
				if err != nil || r["error"] != nil {
					return core.Result{"setup_error": r, "err": err}
				}
				if scenario == "archived-current" {
					for _, op := range []*core.Op{
						{Kind: core.OpSweep, StaleAgents: []string{"worker"}, KeepArchivedNonce: true},
						{Kind: core.OpSweep, KeepArchivedNonce: true},
					} {
						r, err = e.exec(op, time.Now())
						if err != nil || r["error"] != nil {
							return core.Result{"setup_error": r, "err": err}
						}
					}
				}
				l := e.state.Agents["worker"]
				return core.Result{
					"archived": l.Status == core.StatusArchived, "same": l.CreatedSerial == capturedCreated && threadIDOf(l) == wantThread,
					"host": e.remoteHostOf(l), "surface": surfaceOf(l),
				}
			})
			wantHost, wantSurface := "remote", "codex"
			switch scenario {
			case "host":
				wantHost = "current"
			case "archived-current":
				wantHost, wantSurface = "current", harnessenv.ChatGPTApp
			case "local-socket":
				wantHost, wantSurface = "", "cli"
			case "missing-route", "handled-missing-route":
				wantHost = "unattached"
			}
			if err != nil || setup["same"] != true || setup["host"] != wantHost || setup["surface"] != wantSurface || setup["archived"] != (scenario == "archived-current") || serial == 0 {
				t.Fatalf("setup did not change the captured route: %v %v serial=%d", setup, err, serial)
			}
			var got WakeRequest
			if scenario == "surface" || scenario == "host" || scenario == "thread" {
				select {
				case got = <-old:
				case got = <-current:
				case <-time.After(3 * time.Second):
					t.Fatal("fresh route was not handed off")
				}
				// Always report before asserting the request, so the worker joins
				// even on the old-code failure path.
				if !e.ReportWakeResult(WakeResult{ID: got.ID, Host: got.Host, OK: true, NativeDelivery: "accepted"}) {
					t.Fatal("report refused")
				}
			}
			if scenario == "local-socket" {
				wire := make(chan string, 1)
				go func() { wire <- readAll(t, sock) }()
				select {
				case text := <-wire:
					if !strings.Contains(text, "pending-route-marker") {
						t.Fatalf("fresh socket lost the mail digest: %q", text)
					}
				case wrong := <-old:
					e.ReportWakeResult(WakeResult{ID: wrong.ID, Host: wrong.Host, OK: true})
					waitWakeDone(t, e, "worker")
					t.Fatalf("stale remote app handoff instead of current socket: %+v", wrong)
				case <-time.After(3 * time.Second):
					t.Fatal("fresh socket was not reached")
				}
			}
			if scenario == "missing-route" || scenario == "handled-missing-route" || scenario == "archived-current" {
				deadline := time.After(3 * time.Second)
				for {
					select {
					case wrong := <-old:
						e.ReportWakeResult(WakeResult{ID: wrong.ID, Host: wrong.Host, OK: true})
						waitWakeDone(t, e, "worker")
						t.Fatalf("fresh refusal used the stale remote route: %+v", wrong)
					case wrong := <-current:
						e.ReportWakeResult(WakeResult{ID: wrong.ID, Host: wrong.Host, OK: true})
						waitWakeDone(t, e, "worker")
						t.Fatalf("fresh refusal handed off a request: %+v", wrong)
					case <-deadline:
						t.Fatal("fresh refusal did not settle")
					case <-time.After(time.Millisecond):
					}
					r, err := e.query(ctx, func() core.Result {
						e.wakers.mu.Lock()
						defer e.wakers.mu.Unlock()
						return core.Result{"running": e.wakers.running["worker"]}
					})
					if err != nil {
						t.Fatal(err)
					}
					if r["running"] == false {
						break
					}
				}
			}
			waitWakeDone(t, e, "worker")
			wantThread := session
			if scenario == "thread" {
				wantThread = remoteThread
			}
			if got.ID != 0 && (got.Surface != wantSurface || got.Host != wantHost || got.Thread != wantThread) {
				t.Fatalf("captured route survived a real update: %+v", got)
			}
			state, err := e.query(ctx, func() core.Result {
				key := "mail:" + noticeKey("worker", serial)
				e.wakers.mu.Lock()
				deferred := e.wakers.deferred["worker"] != nil
				e.wakers.mu.Unlock()
				return core.Result{
					"stored": e.state.Messages[serial] != nil, "unread": len(e.state.Inbox("worker")),
					"command": e.commandWritten[key], "socket": e.socketWritten[key], "deferred": deferred,
					"alert": e.appBindings["worker"].reason, "unknown": e.nativeUnknown[key],
				}
			})
			wantUnread := 1
			if scenario == "handled-missing-route" {
				wantUnread = 0
			}
			if err != nil || state["stored"] != true || state["unread"] != wantUnread {
				t.Fatalf("transport consumed original mail: %v %v", state, err)
			}
			switch scenario {
			case "surface", "host", "thread":
				if state["command"] != true || state["socket"] != false {
					t.Fatalf("fresh command accounting: %v", state)
				}
			case "local-socket":
				if state["command"] != false || state["socket"] != true {
					t.Fatalf("fresh socket accounting: %v", state)
				}
			case "archived-current":
				if state["alert"] != "archived" || state["deferred"] != false || state["unknown"] != false || state["command"] != false || state["socket"] != false {
					t.Fatalf("known fresh refusal became unknown delivery or retried: %v", state)
				}
			case "handled-missing-route":
				if state["alert"] != "" || state["deferred"] != false || state["command"] != false || state["socket"] != false {
					t.Fatalf("handled mail created a route alert or receipt: %v", state)
				}
			case "missing-route":
				if state["alert"] != "route_changed" || state["deferred"] != false || state["command"] != false || state["socket"] != false {
					t.Fatalf("fresh refusal retried or lost its alert: %v", state)
				}
			}
			select {
			case next := <-old:
				t.Fatalf("second remote handoff: %+v", next)
			case next := <-current:
				t.Fatalf("second current-host handoff: %+v", next)
			default:
			}
		})
	}
}
