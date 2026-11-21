// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
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
	if !ok || len(plan.argv) != 3 || plan.antigravity.PID != 0 {
		t.Fatal("a missing built-in binding discarded configured delivery")
	}
}

func TestSentMailOffersOnlyOneFactualNoticeToVerifiedAntigravityConversation(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000007"
	const body = "private mail never enters the app notice"
	for _, lookupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "unavailable"}[lookupFails], func(t *testing.T) {
			e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
			hub := antigravity.Hub{PID: 140, Start: time.Unix(10, 0)}
			cwd := t.TempDir()
			type attempt struct {
				hub          antigravity.Hub
				session, cwd string
			}
			checked, sent := make(chan attempt, 2), make(chan string, 2)
			prev := prepareAntigravity
			prepareAntigravity = func(_ context.Context, h antigravity.Hub, session, dir string) (
				func(context.Context, string) error, error,
			) {
				checked <- attempt{h, session, dir}
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
			e.BindAntigravity(ctx, res["token"].(string), hub, conversation)
			sender := registerAgent(t, e, "sender")
			contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, To: "worker", Token: sender, MsgType: core.MsgQuestion, Body: body,
			})
			select {
			case got := <-checked:
				if got != (attempt{hub, conversation, cwd}) {
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
			if len(sent) != 0 || len(checked) != 0 {
				t.Fatal("wake submitted without preflight or retried its offer")
			}
		})
	}
}
