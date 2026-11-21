// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
)

func TestMissingAntigravityEvidenceKeepsConfiguredDelivery(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000007"
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		antigravity.Client: {Argv: []string{"delivery-fixture", "{thread}", "{message}"}},
	})
	worker := &core.Agent{
		ID: "worker", CurrentSession: conversation, Agent: &core.AgentInfo{Harness: antigravity.Client},
	}
	plan, ok := e.wakeFor(worker, core.MsgQuestion, core.Event{Type: "message.sent", To: "worker"})
	if !ok || len(plan.argv) != 3 || plan.antigravity != 0 {
		t.Fatal("a missing built-in binding discarded configured delivery")
	}
}

func TestSentMailOffersOnlyOneFactualNoticeToVerifiedAntigravityConversation(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000007"
	const body = "private mail never enters the app notice"
	for _, lookupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "unavailable"}[lookupFails], func(t *testing.T) {
			e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
			cwd := t.TempDir()
			type attempt struct {
				session, cwd string
			}
			checked, sent := make(chan attempt, 2), make(chan string, 2)
			prev := prepareAntigravity
			prepareAntigravity = func(_ context.Context, session, dir string) (
				func(context.Context, string) error, error,
			) {
				checked <- attempt{session, dir}
				if lookupFails {
					return nil, errors.New("fixture hub unavailable")
				}
				return func(_ context.Context, notice string) error {
					sent <- notice
					return nil
				}, nil
			}
			t.Cleanup(func() { prepareAntigravity = prev })
			ctx, cancel := context.WithCancel(t.Context())
			go e.Run(ctx)
			t.Cleanup(func() { cancel(); <-e.Done() })
			res := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", SessionAlias: conversation,
				Agent: &core.AgentInfo{Harness: antigravity.Harness, CWD: cwd, HostID: e.HostID()},
			})
			e.BindAntigravity(ctx, res["token"].(string), conversation)
			sender := registerAgent(t, e, "sender")
			contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, To: "worker", Token: sender, MsgType: core.MsgQuestion, Body: body,
			})
			select {
			case got := <-checked:
				if got != (attempt{conversation, cwd}) {
					t.Fatalf("wake checked the wrong original target: %+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("real send never attempted the bound application route")
			}
			if !lookupFails {
				select {
				case notice := <-sent:
					if notice != "Dibs: a new question is waiting." {
						t.Fatalf("app received a different notice: %q", notice)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("verified send did not issue a notice")
				}
			}
			cancel()
			<-e.Done()
			wantOutcome := "accepted"
			if lookupFails {
				wantOutcome = "not_sent"
			}
			observed := e.wakers.diagnostics["worker"]
			if observed.Route != "antigravity_app" || observed.Outcome != wantOutcome {
				t.Fatalf("diagnostics observed %q/%q, want antigravity_app/%s",
					observed.Route, observed.Outcome, wantOutcome)
			}
			if len(sent) != 0 || len(checked) != 0 {
				t.Fatal("wake submitted without preflight or retried its offer")
			}
		})
	}
}

// Reconstruct a fresh daemon from the same records the running engine writes.
// The only calls in the new process are from the sender and the wake worker.
func TestAntigravityWakeSurvivesDaemonReplayWithoutRecipientCheckIn(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000093"
	const node = "antigravity-restart"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	records := &queueReplayLedger{}
	first := New(core.NewState(node, core.DefaultLimits()), records, deadProber{})
	go first.Run(ctx)
	defer func() { cancel(); <-first.Done() }()
	worker := contactReplayDo(t, first, ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", SessionAlias: conversation,
		Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: first.HostID()},
	})
	first.BindAntigravity(ctx, worker["token"].(string), conversation)
	sender := registerAgent(t, first, "sender")
	cancel()
	<-first.Done()

	replayed := core.NewState(node, core.DefaultLimits())
	for _, entry := range records.records {
		var op core.Op
		if err := json.Unmarshal(entry.raw, &op); err != nil {
			t.Fatal("record decode:", err)
		}
		if _, _, err := replayed.Apply(&op, entry.at); err != nil {
			t.Fatal("record replay:", err)
		}
	}
	sent := make(chan string, 1)
	previous := prepareAntigravity
	prepareAntigravity = func(_ context.Context, id, _ string) (func(context.Context, string) error, error) {
		if id != conversation {
			return nil, errors.New("fixture received a different conversation")
		}
		return func(_ context.Context, notice string) error { sent <- notice; return nil }, nil
	}
	defer func() { prepareAntigravity = previous }()
	next := New(replayed, records, deadProber{})
	restartCtx, stop := context.WithCancel(t.Context())
	go next.Run(restartCtx)
	defer func() { stop(); <-next.Done() }()
	contactReplayDo(t, next, restartCtx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "restart fixture",
	})
	select {
	case notice := <-sent:
		if notice != "Dibs: a new question is waiting." {
			t.Fatalf("replayed route sent %q", notice)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("verified app conversation lost its wake route across restart")
	}
}
