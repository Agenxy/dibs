// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	for _, tc := range []struct {
		name, outcome, note    string
		lookupFails, uncertain bool
	}{
		{name: "accepted", outcome: "accepted", note: "app accepted the last Antigravity wake"},
		{name: "unavailable", outcome: "not_sent", note: "last Antigravity wake stopped before submission", lookupFails: true},
		{name: "unknown", outcome: "unknown", note: "App acceptance of the last Antigravity wake is unknown", uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				if tc.lookupFails {
					return nil, errors.New("fixture hub unavailable")
				}
				return func(_ context.Context, notice string) error {
					sent <- notice
					if tc.uncertain {
						return errors.New("fixture lost the app response")
					}
					return nil
				}, nil
			}
			t.Cleanup(func() { prepareAntigravity = prev })
			ctx, cancel := context.WithCancel(t.Context())
			go e.Run(ctx)
			t.Cleanup(func() { cancel(); <-e.Done() })
			res := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", SessionAlias: conversation,
				AgentKind: core.KindPersistent,
				Agent:     &core.AgentInfo{Harness: antigravity.Harness, CWD: cwd, HostID: e.HostID()},
			})
			worker := res["token"].(string)
			e.BindAntigravity(ctx, worker, conversation)
			sender := registerAgent(t, e, "sender")
			sleepAntigravitySendRecipient(t, e, ctx)
			send := func() core.Result {
				t.Helper()
				return contactReplayDo(t, e, ctx, &core.Op{
					Kind: core.OpSendMessage, To: "worker", Token: sender, MsgType: core.MsgQuestion,
					Body: body, OpID: "test-antigravity-wake-note",
				})
			}
			sentMail := send()
			if note, _ := sentMail["note"].(string); !strings.Contains(note, "Antigravity conversation") ||
				!strings.Contains(note, "in progress") || strings.Contains(note, "when it next wakes") {
				t.Errorf("bound Antigravity send did not describe its wake attempt: %q", note)
			}
			select {
			case got := <-checked:
				if got != (attempt{conversation, cwd}) {
					t.Fatalf("wake checked the wrong original target: %+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("real send never attempted the bound application route")
			}
			if !tc.lookupFails {
				select {
				case notice := <-sent:
					if notice != "Dibs: a new question is waiting." {
						t.Fatalf("app received a different notice: %q", notice)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("verified send did not issue a notice")
				}
			}
			waitWakeDone(t, e, "worker")
			serial := sentMail["msg_serial"].(uint64)
			if tc.lookupFails {
				_, err := e.query(ctx, func() core.Result {
					m := e.state.Messages[serial]
					if m == nil || m.ContactEscalatedAt == 0 {
						t.Error("refused-before-submission wake left unread mail without a contact alert")
					} else {
						notice, _ := e.contactNotice(e.state.Contacts[m.ContactEscalatedAt])
						if notice.Contact == nil || !strings.Contains(notice.Contact.OpenHint, "Antigravity") ||
							!strings.Contains(notice.Contact.OpenHint, conversation) {
							t.Errorf("contact did not identify the original conversation: %+v", notice.Contact)
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			duplicate := send()
			if duplicate["deduplicated"] != true || duplicate["msg_serial"] != serial {
				t.Fatalf("setup: second send did not reuse its original mail: %+v", duplicate)
			}
			note, _ := duplicate["note"].(string)
			if !strings.Contains(note, tc.note) || strings.Contains(note, "in progress") {
				t.Errorf("send advisory after observation = %q, want %q", note, tc.note)
			}
			e.BindAntigravity(ctx, worker, "")
			e.BindAntigravity(ctx, worker, conversation)
			if note := e.SendDeliveryNoteFor(ctx, "worker", serial); !strings.Contains(note, "conversation is unknown") ||
				strings.Contains(note, "last Antigravity wake") {
				t.Errorf("a renewed binding inherited its old wake outcome: %q", note)
			}
			cancel()
			<-e.Done()
			observed := e.wakers.diagnostics["worker"]
			if observed.Route != "antigravity_app" || observed.Outcome != tc.outcome {
				t.Fatalf("diagnostics observed %q/%q, want antigravity_app/%s",
					observed.Route, observed.Outcome, tc.outcome)
			}
			if len(sent) != 0 || len(checked) != 0 {
				t.Fatal("wake submitted without preflight or retried its offer")
			}
		})
	}
}

func sleepAntigravitySendRecipient(t *testing.T, e *Engine, ctx context.Context) {
	t.Helper()
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}})
	if _, err := e.query(ctx, func() core.Result {
		if l := e.state.Agents["worker"]; l == nil || !l.Sleeping() {
			t.Error("setup: the recipient is not sleeping")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSendToUnboundAntigravityKeepsTheExistingHint(t *testing.T) {
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(t.Context())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.Done() })
	contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", AgentKind: core.KindPersistent,
		Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
	})
	sender := registerAgent(t, e, "sender")
	sleepAntigravitySendRecipient(t, e, ctx)
	sent := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSendMessage, To: "worker", Token: sender, MsgType: core.MsgQuestion, Body: "unbound fixture",
	})
	if note, _ := sent["note"].(string); !strings.Contains(note, antigravityUnbound) {
		t.Fatalf("unbound Antigravity wake hint = %q", note)
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

func TestAntigravityRefusalDoesNotEscalateSettledAttempt(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000094"
	for _, settled := range []string{"withdrawn", "read", "rebound"} {
		t.Run(settled, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			previous := prepareAntigravity
			prepareAntigravity = func(ctx context.Context, _, _ string) (func(context.Context, string) error, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil, errors.New("fixture missing workspace")
			}
			t.Cleanup(func() { prepareAntigravity = previous })
			e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(t.Context())
			go e.Run(ctx)
			t.Cleanup(func() { cancel(); <-e.Done() })
			worker := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", SessionAlias: conversation, AgentKind: core.KindPersistent,
				Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
			})["token"].(string)
			e.BindAntigravity(ctx, worker, conversation)
			sender := registerAgent(t, e, "sender")
			sleepAntigravitySendRecipient(t, e, ctx)
			serial := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "fixture",
			})["msg_serial"].(uint64)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("real send did not enter app preflight")
			}
			switch settled {
			case "withdrawn":
				contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRespond, Token: sender, MsgSerial: serial, Disposition: "withdraw"})
			case "read":
				if _, err := e.ReadMail(ctx, worker, serial, true); err != nil {
					t.Fatal(err)
				}
			case "rebound":
				e.BindAntigravity(ctx, worker, "")
				e.BindAntigravity(ctx, worker, conversation)
			}
			close(release)
			waitWakeDone(t, e, "worker")
			if _, err := e.query(ctx, func() core.Result {
				if len(e.state.Contacts) != 0 {
					t.Error("stale preflight failure escalated settled mail/binding")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
