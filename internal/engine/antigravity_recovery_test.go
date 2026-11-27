// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
)

func TestNewAntigravityWakeEscalatesUnreadEarlierAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     string
		priority string
		contact  bool
	}{
		{"question", core.MsgQuestion, "", true},
		{"ordinary-notify", core.MsgNotify, "normal", false},
		{"high-notify", core.MsgNotify, "high", true},
		{"urgent-notify", core.MsgNotify, "urgent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testAntigravityEarlierAcceptance(t, tc.kind, tc.priority, tc.contact)
		})
	}
}

func TestAntigravityHubReturnFencesInFlightAcceptance(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000098"
	callbacks := make(chan func(string), 1)
	oldWatch := watchAntigravityHub
	watchAntigravityHub = func(ctx context.Context, changed func(string)) error {
		callbacks <- changed
		<-ctx.Done()
		return nil
	}
	t.Cleanup(func() { watchAntigravityHub = oldWatch })
	entered, release, accepted := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	var calls atomic.Int32
	oldPrepare := prepareAntigravity
	prepareAntigravity = func(context.Context, string, string) (func(context.Context, string) error, error) {
		return func(ctx context.Context, _ string) error {
			if calls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			accepted <- struct{}{}
			return nil
		}, nil
	}
	t.Cleanup(func() { prepareAntigravity = oldPrepare })
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(t.Context())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.Done() })
	worker := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", AgentKind: core.KindPersistent, SessionAlias: conversation,
		Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
	})["token"].(string)
	e.BindAntigravity(ctx, worker, conversation)
	var change func(string)
	select {
	case change = <-callbacks:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: production binding did not install observer")
	}
	sender := registerAgent(t, e, "sender")
	sleepAntigravitySendRecipient(t, e, ctx)
	serial := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "in flight",
	})["msg_serial"].(uint64)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: original native submission did not enter")
	}
	change("replacement-hub")
	close(release)
	for range 2 {
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("old in-flight acceptance suppressed the replacement-hub offer")
		}
	}
	waitWakeDone(t, e, "worker")
	_, err := e.query(ctx, func() core.Result {
		cohort := e.antigravityAccepted["worker"]
		if cohort.epoch != "replacement-hub" || !cohort.items[serial] {
			t.Error("old receipt displaced the new incarnation's acceptance cohort")
		}
		if m := e.state.Messages[serial]; m.DeliveredAt != 0 || m.ContactEscalatedAt != 0 {
			t.Error("in-flight restart fabricated mail delivery or failure")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testAntigravityEarlierAcceptance(t *testing.T, kind string, priority string, contact bool) {
	t.Helper()
	const conversation = "01a10520-0000-7000-8000-000000000095"
	accepted := make(chan struct{}, 2)
	previous := prepareAntigravity
	prepareAntigravity = func(context.Context, string, string) (func(context.Context, string) error, error) {
		return func(context.Context, string) error { accepted <- struct{}{}; return nil }, nil
	}
	t.Cleanup(func() { prepareAntigravity = previous })
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(t.Context())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.Done() })
	worker := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", AgentKind: core.KindPersistent,
		SessionAlias: conversation, Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
	})["token"].(string)
	e.BindAntigravity(ctx, worker, conversation)
	sender := registerAgent(t, e, "sender")
	sleepAntigravitySendRecipient(t, e, ctx)
	first := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: kind, RequestPriority: priority, Body: "first"})["msg_serial"].(uint64)
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: first notice was not accepted")
	}
	waitWakeDone(t, e, "worker")
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[first]
		if m == nil || m.DeliveredAt != 0 || m.ContactEscalatedAt != 0 {
			t.Error("first acceptance was mistaken for mail delivery or failure")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "second"})
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: a second wake was not accepted")
	}
	waitWakeDone(t, e, "worker")
	_, err = e.query(ctx, func() core.Result {
		m := e.state.Messages[first]
		if got := m.ContactEscalatedAt != 0; got != contact {
			t.Errorf("later actual acceptance contact=%v want %v", got, contact)
		}
		if m.ContactEscalatedAt != 0 {
			n, _ := e.contactNotice(e.state.Contacts[m.ContactEscalatedAt])
			if n.Contact == nil || !strings.Contains(n.Contact.OpenHint, conversation) {
				t.Error("contact omitted original conversation")
			}
		}
		if e.state.Messages[first].DeliveredAt != 0 {
			t.Error("contact marked mail delivered")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Run and BindAntigravity install the observer. The fixture replaces only
// the native filesystem edge and app receipt; no setter is called by hand.
func TestAntigravityHubReturnReoffersOnlyUnreadBoundMail(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000096"
	for _, settled := range []string{"unread", "read", "withdrawn", "unbound", "retired"} {
		t.Run(settled, func(t *testing.T) {
			callbacks := make(chan func(string), 1)
			oldWatch := watchAntigravityHub
			watchAntigravityHub = func(ctx context.Context, changed func(string)) error {
				callbacks <- changed
				<-ctx.Done()
				return nil
			}
			t.Cleanup(func() { watchAntigravityHub = oldWatch })
			accepted := make(chan struct{}, 4)
			oldPrepare := prepareAntigravity
			prepareAntigravity = func(context.Context, string, string) (func(context.Context, string) error, error) {
				return func(context.Context, string) error { accepted <- struct{}{}; return nil }, nil
			}
			t.Cleanup(func() { prepareAntigravity = oldPrepare })
			e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(t.Context())
			go e.Run(ctx)
			t.Cleanup(func() { cancel(); <-e.Done() })
			worker := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", AgentKind: core.KindPersistent,
				SessionAlias: conversation, Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
			})["token"].(string)
			e.BindAntigravity(ctx, worker, conversation)
			var change func(string)
			select {
			case change = <-callbacks:
			case <-time.After(5 * time.Second):
				t.Fatal("production binding did not start hub observation")
			}
			sender := registerAgent(t, e, "sender")
			sleepAntigravitySendRecipient(t, e, ctx)
			serial := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "held"})["msg_serial"].(uint64)
			select {
			case <-accepted:
			case <-time.After(5 * time.Second):
				t.Fatal("setup: no initial app acceptance")
			}
			waitWakeDone(t, e, "worker")
			switch settled {
			case "read":
				if _, err := e.ReadMail(ctx, worker, serial, true); err != nil {
					t.Fatal(err)
				}
			case "withdrawn":
				contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRespond, Token: sender, MsgSerial: serial, Disposition: "withdraw"})
			case "unbound":
				e.BindAntigravity(ctx, worker, "")
			case "retired":
				contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSignOff, Token: worker})
			}
			change("new-hub")
			if settled == "unread" {
				select {
				case <-accepted:
				case <-time.After(5 * time.Second):
					t.Fatal("hub change lost accepted unread mail")
				}
				waitWakeDone(t, e, "worker")
				_, err := e.query(ctx, func() core.Result {
					m := e.state.Messages[serial]
					if m.DeliveredAt != 0 || m.ContactEscalatedAt != 0 {
						t.Error("restart offer fabricated delivery or a failed turn")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			change("new-hub") // duplicate filesystem observations do not buy another turn
			select {
			case <-accepted:
				t.Fatal("settled mail or repeated hub observation submitted another notice")
			case <-time.After(150 * time.Millisecond):
			}
		})
	}
}
