// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/peerwake"
)

type noContactDesktop struct{}

func (noContactDesktop) Available() bool { return false }
func (noContactDesktop) Ask(humanask.Message) (humanask.Answer, error) {
	panic("test must not ask the real desktop")
}

func contactEngine(t *testing.T, recipient *core.Agent) (*Engine, uint64) {
	t.Helper()
	now := time.Now().Add(-time.Hour)
	s := core.NewState("contact-route", core.DefaultLimits())
	for _, name := range []string{"asker", "closed"} {
		_, _, err := s.Apply(&core.Op{
			Kind: core.OpRegister, Name: name, NewToken: "tok-" + name,
			AgentKind: core.KindPersistent, Nonce: "nonce-" + name,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	base := s.Agents["closed"]
	base.SessionID = recipient.SessionID
	base.SessionAliases = recipient.SessionAliases
	base.Agent = recipient.Agent
	base.Status = recipient.Status
	res, _, err := s.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-asker", To: "closed",
		MsgType: core.MsgRequest, Body: "private content must never appear in contact",
		DeadlineSec: 3600, DeliveryStart: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	e := New(s, &memLedger{}, deadProber{})
	e.SetHumanNotifier(noContactDesktop{})
	return e, res["msg_serial"].(uint64)
}

func TestNoRouteEscalatesThroughPublishedSendAndKeepsBodyPrivate(t *testing.T) {
	e, serial := contactEngine(t, &core.Agent{
		Status: core.StatusDormant,
		Agent:  &core.AgentInfo{Harness: "terminal harness"},
	})
	if e.hasWakeRoute(e.state.Agents["closed"]) {
		t.Fatal("setup has a wake route")
	}
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	if len(e.state.Contacts) != 1 || e.state.Messages[serial].ContactEscalatedAt == 0 {
		t.Fatalf("published send with no route did not escalate: %+v", e.state.Contacts)
	}
	for _, c := range e.state.Contacts {
		n, _ := e.contactNotice(c)
		if n.Contact == nil || n.Contact.Message != serial ||
			strings.Contains(n.Contact.OpenHint, "private content") {
			t.Fatalf("contact notice copied participant body or lost source: %+v", n)
		}
	}
}

func TestRecentActiveAgentWithoutWakeRouteGetsARecheckBeforeHumanContact(t *testing.T) {
	e, serial := contactEngine(t, &core.Agent{
		Status: core.StatusActive,
		Agent:  &core.AgentInfo{Harness: "terminal harness"},
	})
	l := e.state.Agents["closed"]
	e.seen["closed"] = time.Now().Add(-100 * time.Second)
	if e.recentlyInTouch(l) || !e.contactLooksLive(l) || e.hasWakeRoute(l) {
		t.Fatal("setup: expected active grace beyond the ordinary recency window, with no route")
	}
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	if len(e.state.Contacts) != 0 {
		t.Fatal("active recipient's ongoing turn was mistaken for an unreachable closed harness")
	}
	e.wakers.mu.Lock()
	deferred := e.wakers.deferred["closed"]
	if deferred != nil {
		deferred.Stop()
	}
	e.wakers.mu.Unlock()
	if deferred == nil {
		t.Fatal("active grace did not arm a recheck")
	}
}

func TestStaleSocketMissDefersBeforeEscalatingOnRefresh(t *testing.T) {
	e, serial := contactEngine(t, &core.Agent{
		Status:    core.StatusDormant,
		SessionID: "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256",
		Agent:     &core.AgentInfo{Harness: "Claude Code"},
	})
	e.peers.mu.Lock()
	e.peers.at = time.Now()
	e.peers.live = map[string]peerwake.Session{}
	e.peers.mu.Unlock()
	if !e.socketMayHaveAppeared(e.state.Agents["closed"]) {
		t.Fatal("setup did not enter stale-socket retry path")
	}
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	if len(e.state.Contacts) != 0 {
		t.Fatal("snapshot miss was treated as a confirmed no-route verdict")
	}
	e.wakers.mu.Lock()
	deferred := e.wakers.deferred["closed"]
	if deferred != nil {
		deferred.Stop()
	}
	e.wakers.mu.Unlock()
	if deferred == nil {
		t.Fatal("stale cache did not arm its refresh")
	}
	e.retryWakeDecision("closed")
	if len(e.state.Contacts) != 1 {
		t.Fatalf("refreshed no-route verdict did not escalate: %+v", e.state.Contacts)
	}
	e.wakers.mu.Lock()
	if retry := e.wakers.deferred["closed"]; retry != nil {
		retry.Stop()
	}
	e.wakers.mu.Unlock()
}

func TestDormantClaudeAppRowWithoutThreadEscalatesOnRetry(t *testing.T) {
	e, _ := contactEngine(t, &core.Agent{
		Status: core.StatusDormant,
		Agent:  &core.AgentInfo{Harness: "Claude Code", Surface: "claude-desktop"},
	})
	if threadIDOf(e.state.Agents["closed"]) != "" || e.hasWakeRoute(e.state.Agents["closed"]) {
		t.Fatal("setup: expected a closed app row with no thread and no wake route")
	}
	e.retryWakeDecision("closed")
	if len(e.state.Contacts) != 1 {
		t.Fatalf("app row without an openable thread was skipped: %+v", e.state.Contacts)
	}
	e.wakers.mu.Lock()
	if retry := e.wakers.deferred["closed"]; retry != nil {
		retry.Stop()
	}
	e.wakers.mu.Unlock()
}

func TestContactStaysOutstandingWithoutDesktopAndRelayReceiptSettlesIt(t *testing.T) {
	e, serial := contactEngine(t, &core.Agent{
		Status: core.StatusDormant,
		Agent:  &core.AgentInfo{Harness: "terminal harness"},
	})
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	pending, err := e.PendingForHuman(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Type != "contact" || pending[0].Contact == nil {
		t.Fatalf("relay attach did not recover an outstanding contact: %+v", pending)
	}
	contact := pending[0].Serial
	if !e.state.Contacts[contact].NotifiedAt.IsZero() {
		t.Fatal("mere relay fetch falsely marked contact posted")
	}
	if err := e.ReportHumanDelivery(ctx, contact, "relay-fixture", "posted", ""); err != nil {
		t.Fatal(err)
	}
	if e.state.Contacts[contact].NotifiedAt.IsZero() {
		t.Fatal("real posting receipt did not settle the contact")
	}
	again, err := e.PendingForHuman(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range again {
		if n.Serial == contact {
			t.Fatal("posted contact still offered as pending")
		}
	}
}

func TestContactSnapshotIncludesMessagesCoalescedAfterScheduling(t *testing.T) {
	e, serial := contactEngine(t, &core.Agent{
		Status: core.StatusDormant,
		Agent:  &core.AgentInfo{Harness: "terminal harness"},
	})
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	contact := e.state.Messages[serial].ContactEscalatedAt
	if contact == 0 {
		t.Fatal("setup: no contact scheduled")
	}
	_, _, err := e.state.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-asker",
		To: "closed", MsgType: core.MsgQuestion, Body: "second private message",
		DeliveryStart: true,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.escalateContact("closed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	n, _, ok := e.contactSnapshot(contact)
	if !ok || n.Contact == nil || n.Contact.Count != 2 {
		t.Fatalf("delayed snapshot lost coalesced count: %+v", n)
	}
}

func TestRecipientReadResolvesContactAndLedgersCleanupState(t *testing.T) {
	e, serial := contactEngine(t, &core.Agent{
		Status: core.StatusDormant,
		Agent:  &core.AgentInfo{Harness: "terminal harness"},
	})
	e.maybeWake(core.Event{
		Type: "message.sent", To: "closed", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgRequest, "msg_serial": serial},
	})
	contact := e.state.Messages[serial].ContactEscalatedAt
	if contact == 0 {
		t.Fatal("setup: no contact to resolve")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	_, err := e.query(ctx, func() core.Result {
		if _, err := e.applyAndLedger(&core.Op{
			Kind:       core.OpMarkDelivered,
			MsgSerials: []uint64{serial},
		}, time.Now()); err != nil {
			t.Error(err)
		}
		return core.Result{"resolved": !e.state.Contacts[contact].ResolvedAt.IsZero()}
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.state.Contacts[contact].ResolvedAt.IsZero() {
		t.Fatal("recipient read left obsolete human contact outstanding")
	}
	ledger := e.led.(*memLedger)
	found := false
	for _, op := range ledger.ops {
		if op.Kind == core.OpContactResolved && op.ContactSerial == contact {
			found = true
		}
	}
	if !found {
		t.Fatal("resolved contact changed state without a replayable ledger op")
	}
}

func TestContactLinkRequiresAppProvenance(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	for _, tc := range []struct {
		surface string
		hostID  string
		want    bool
	}{
		{"", "", false},
		{"chatgpt-app", "", true},
		{"chatgpt-app", "remote-machine", false},
	} {
		e, serial := contactEngine(t, &core.Agent{
			Status:    core.StatusDormant,
			SessionID: thread, Agent: &core.AgentInfo{
				Harness: "Codex", Surface: tc.surface,
				HostID: tc.hostID,
			},
		})
		c := &core.ContactEscalation{Serial: 100, Recipient: "closed", OldestSerial: serial, HighWater: serial, Count: 1}
		n, plan := e.contactNotice(c)
		enrichContactURL(&n, plan)
		if got := n.Contact.OpenURL != ""; got != tc.want {
			t.Fatalf("surface=%q host=%q link=%q; want link=%v", tc.surface, tc.hostID, n.Contact.OpenURL, tc.want)
		}
	}
}

func TestNeverDeliveredExpiryNotifiesTheSender(t *testing.T) {
	who, notice, blocking := situationalNotice(core.Event{
		Type: "message.expired_recipient_dormant", To: "asker",
		Data: map[string]any{
			"msg_serial": uint64(42), "never_delivered": true,
			"detail": "never delivered: recipient stayed closed",
		},
	})
	if who != "asker" || !blocking || !strings.Contains(notice, "42") ||
		!strings.Contains(notice, "never delivered") {
		t.Fatalf("sender did not get honest expiry notice: %q %q %v", who, notice, blocking)
	}
}
