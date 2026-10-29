// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Enter at the planner, with the same sidecar and listening socket as a real
// harness. In particular, do not set the plan's binding evidence by hand.
func TestSocketBindingPrecedenceThroughPlanningAndDelivery(t *testing.T) {
	for _, scenario := range []struct {
		name, cwd, host             string
		guessed, collision, deliver bool
	}{
		{"stated-moved", "/work/moved", "t", false, false, true},
		{"stated-same", "/w", "t", false, false, true},
		{"guessed-same", "/w", "t", true, false, true},
		{"unknown-host-same", "/w", "", false, false, true},
		{"guessed-moved", "/work/moved", "t", true, false, false},
		{"unknown-host-moved", "/work/moved", "", false, false, false},
		{"other-host", "/w", "another-machine", false, false, false},
		{"two-stated-same-directory", "/w", "t", false, true, false},
		{"guessed-collision", "/w", "t", true, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sock, sid := listeningSession(t)
			st := core.NewState("t", core.DefaultLimits())
			e := New(st, &memLedger{}, deadProber{})
			now := t0Engine()
			apply := func(op *core.Op) core.Result {
				t.Helper()
				res, _, err := st.Apply(op, now)
				if err != nil {
					t.Fatal("setup:", err)
				}
				return res
			}
			primary := sid
			if scenario.guessed {
				primary = "host-worker"
			}
			apply(&core.Op{
				Kind: core.OpRegister, Name: "worker", NewToken: "worker-token",
				SessionID: primary, SessionAlias: sid, SessionGuessed: scenario.guessed,
				Agent: &core.AgentInfo{Harness: "Claude Code", CWD: scenario.cwd, HostID: scenario.host},
			})
			worker := st.Agents["worker"]
			if worker == nil || !worker.HoldsSession(sid) || worker.GuessedSession(sid) != scenario.guessed {
				t.Fatal("setup: registration did not create the intended binding")
			}
			if scenario.collision {
				apply(&core.Op{
					Kind: core.OpRegister, Name: "other", NewToken: "other-token", SessionID: "other-session",
					Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w", HostID: "t"},
				})
				// Fold a historical bind: today's ingress prevents creating two
				// stated holders, but replay must still tolerate old accepted ops.
				apply(&core.Op{Kind: core.OpBindSession, Token: "other-token", SessionID: sid})
				if !worker.HoldsSession(sid) || !st.Agents["other"].HoldsSession(sid) || st.Agents["other"].GuessedSession(sid) {
					t.Fatal("setup: no real conflicting holder")
				}
			}
			apply(&core.Op{Kind: core.OpAckBoard, Token: "worker-token"})
			e.primePeerSessions()
			plan, planned := e.wakeFor(worker, core.MsgQuestion, core.Event{
				Type: "message.sent", To: "worker",
				Data: map[string]any{"msg_type": core.MsgQuestion, "from": "sender"},
			})
			// A remote agent may be rejected before the local socket planner.
			if !planned && scenario.host == "another-machine" {
				return
			}
			if !planned || len(plan.argv) != 0 {
				t.Fatal("setup: no socket plan")
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			got := make(chan string, 1)
			go func() { got <- readAll(t, sock) }()
			if delivered := e.wakeOverSocket(plan, "worker"); delivered != scenario.deliver {
				t.Errorf("delivery=%v, want %v; log=%s", delivered, scenario.deliver, logs.String())
			}
			if scenario.deliver {
				select {
				case wire := <-got:
					if !strings.Contains(wire, "Dibs") {
						t.Errorf("empty socket notice: %q", wire)
					}
				case <-time.After(time.Second):
					t.Error("no socket write")
				}
				if scenario.cwd != "/w" && !strings.Contains(logs.String(), "stated binding outranks cwd difference") {
					t.Error("allowance omitted its evidence")
				}
				return
			}
			select {
			case wire := <-got:
				t.Errorf("refused session received bytes: %q", wire)
			case <-time.After(30 * time.Millisecond):
			}
			res, err := e.exec(&core.Op{Kind: core.OpAckBoard, Token: "worker-token", MailboxPage: true}, now)
			if err != nil {
				t.Fatal("checkpoint:", err)
			}
			view, _ := res["socket_wake"].(core.Result)
			if view["state"] != "refused" || view["reason"] == "" || !strings.Contains(view["hint"].(string), "bind_session") {
				t.Errorf("own check_in hides refusal and repair: %v", res["socket_wake"])
			}
			board := e.decoratedBoard()
			found := false
			for _, row := range board["agents"].([]map[string]any) {
				if row["id"] == "worker" {
					found = row["socket_wake"] != nil
				}
			}
			if !found {
				t.Error("doctor's board has no socket refusal")
			}
		})
	}
}

func TestSocketBindingIsRecheckedBeforeProductionOffer(t *testing.T) {
	for _, collisionAfterPlanning := range []bool{false, true} {
		t.Run(map[bool]string{false: "stated-moved", true: "collision-after-planning"}[collisionAfterPlanning], func(t *testing.T) {
			sock, sid := listeningSession(t)
			st := core.NewState("t", core.DefaultLimits())
			cwd := "/work/moved"
			if collisionAfterPlanning {
				cwd = "/w"
			}
			for _, id := range []string{"worker", "other", "sender"} {
				session := id + "-session"
				if id == "worker" {
					session = sid
				}
				if _, _, err := st.Apply(&core.Op{
					Kind: core.OpRegister, Name: id, NewToken: id + "-token", SessionID: session,
					Agent: &core.AgentInfo{Harness: "Claude Code", CWD: cwd, HostID: "t"},
				}, time.Now()); err != nil {
					t.Fatal("setup:", err)
				}
			}
			e := New(st, &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			go e.Run(ctx)
			stopWakeTimersOnCleanup(t, e)
			t.Cleanup(func() { cancel(); <-e.Done() })
			var plan wakePlan
			_, err := e.query(ctx, func() core.Result {
				// Setup mail without publishing a concurrent automatic wake.
				if _, _, err := st.Apply(&core.Op{
					Kind: core.OpSendMessage, Token: "sender-token", To: "worker",
					MsgType: core.MsgQuestion, Body: "production-binding-marker",
				}, time.Now()); err != nil {
					t.Error("setup:", err)
				}
				var ok bool
				plan, ok = e.wakeFor(st.Agents["worker"], core.MsgQuestion, questionFor("worker"))
				if !ok || len(plan.argv) != 0 {
					t.Error("setup: no socket plan")
				}
				if collisionAfterPlanning {
					if _, _, err := st.Apply(&core.Op{Kind: core.OpBindSession, Token: "other-token", SessionID: sid}, time.Now()); err != nil {
						t.Error("setup:", err)
					}
					if !st.Agents["worker"].HoldsSession(sid) || !st.Agents["other"].HoldsSession(sid) {
						t.Error("setup: bind did not create a collision")
					}
				}
				return nil
			})
			if err != nil || t.Failed() {
				t.Fatalf("setup query: %v", err)
			}
			wire := make(chan string, 1)
			go func() { wire <- readAll(t, sock) }()
			if delivered := e.runWakeAndReport(plan, "worker"); delivered == collisionAfterPlanning {
				t.Errorf("production delivery=%v with late collision=%v", delivered, collisionAfterPlanning)
			}
			select {
			case text := <-wire:
				if collisionAfterPlanning || !strings.Contains(text, "production-binding-marker") {
					t.Errorf("unexpected socket bytes: %q", text)
				}
			case <-time.After(100 * time.Millisecond):
				if !collisionAfterPlanning {
					t.Error("production offer did not reach the socket")
				}
			}
		})
	}
}
