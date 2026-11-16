// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

type reopenContactFixture struct {
	e         *Engine
	ctx       context.Context
	cancel    context.CancelFunc
	ledger    *memLedger
	opened    chan []string
	recipient string
	sender    string
	created   uint64
	joined    chan struct{}
}

// All app and process observations belong to this fixture; the send, wake
// decision, open callback and eventual contact op follow production paths.
func newReopenContactFixture(t *testing.T) reopenContactFixture {
	t.Helper()
	const cli = "6336088d-1111-4222-8333-444455556666"
	const local = "local_08697f65-6786-4c61-96f8-9af1aa07b24e"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "a", "o")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{"sessionId": local, "cliSessionId": cli, "isArchived": false})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, local+".json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	app := &fakeApp{}
	app.install(t)
	opened := make(chan []string, 4)
	shower.Open = func(argv []string) error { opened <- argv; return nil }
	led := &memLedger{}
	e := New(core.NewState("reopen-contact", core.DefaultLimits()), led, deadProber{})
	e.SetHumanNotifier(noContactDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	stopWakeTimersOnCleanup(t, e)
	f := reopenContactFixture{e: e, ctx: ctx, cancel: cancel, ledger: led, opened: opened, joined: joined}
	worker := f.do(t, &core.Op{
		Kind: core.OpRegister, Name: "lead", Nonce: "reopen-contact-lead",
		SessionID: cli, AgentKind: core.KindPersistent,
		Agent: &core.AgentInfo{Harness: "Claude Code", Surface: harnessenv.ClaudeDesktop, CWD: t.TempDir()},
	})
	f.recipient = worker["token"].(string)
	if worker["agent_id"] != "lead" {
		t.Fatalf("setup: unexpected recipient: %v", worker)
	}
	f.do(t, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"lead"}})
	f.sender = f.do(t, &core.Op{Kind: core.OpRegister, Name: "asker", Nonce: "reopen-contact-asker"})["token"].(string)
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.sender})
	snapshot, err := e.query(ctx, func() core.Result {
		return core.Result{"created": e.state.Agents["lead"].CreatedSerial, "status": e.state.Agents["lead"].Status}
	})
	if err != nil || snapshot["status"] != core.StatusDormant {
		t.Fatalf("setup: recipient was not dormant: %v / %v", snapshot, err)
	}
	f.created = snapshot["created"].(uint64)
	return f
}

func (f reopenContactFixture) do(t *testing.T, op *core.Op) core.Result {
	t.Helper()
	r, err := f.e.Do(f.ctx, op)
	if err != nil || r["error"] != nil {
		t.Fatalf("setup %s failed: %v / %v", op.Kind, r, err)
	}
	return r
}

func TestEndedClaudeWaitsWithoutNavigatingAndRetainsMail(t *testing.T) {
	for _, kind := range []string{core.MsgRequest, core.MsgNotify} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { testEndedClaudeWaitsWithoutNavigatingAndRetainsMail(t, kind) })
		})
	}
}

func testEndedClaudeWaitsWithoutNavigatingAndRetainsMail(t *testing.T, kind string) {
	f := newReopenContactFixture(t)
	const private = "PRIVATE-BACKGROUND-WAIT-MAIL"
	sent := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: kind, Body: private})
	// The real send goes through the finite arrival batch. Observe its
	// published result rather than calling the wake decision by hand.
	synctest.Sleep(time.Second)
	synctest.Wait()
	board, err := f.e.Board(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	alerts, ok := board["app_binding_alerts"].([]core.Result)
	if !ok || len(alerts) != 1 || alerts[0]["reason"] != "waiting-thread-open" {
		diagnosis, err := f.e.query(f.ctx, func() core.Result {
			l := f.e.state.Agents["lead"]
			return core.Result{
				"status": l.Status, "surface": surfaceOf(l), "live": f.e.contactLooksLive(l),
				"route": f.e.hasWakeRoute(l), "socket": f.e.socketOwnsDelivery(l), "mail": f.e.hasBlockingMail(l.ID),
				"policy": f.e.WakePolicy(),
			}
		})
		t.Fatalf("ended session has no honest wait diagnosis: %v setup=%v err=%v", board["app_binding_alerts"], diagnosis, err)
	}
	found := false
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == "lead" {
			found = row["app_wait_reason"] == "waiting for its thread to be opened"
		}
	}
	if !found {
		t.Fatal("board row omitted the waiting reason")
	}
	raw, err := json.Marshal(board)
	if err != nil || strings.Contains(string(raw), private) {
		t.Fatal("board leaked private mail")
	}
	select {
	case argv := <-f.opened:
		t.Fatalf("ended Claude session navigated: %v", argv)
	default:
	}
	serial := sent["msg_serial"].(uint64)
	snapshot, err := f.e.query(f.ctx, func() core.Result {
		m := f.e.state.Messages[serial]
		return core.Result{"retained": m != nil && m.Body == private, "contacts": len(f.e.state.Contacts)}
	})
	if err != nil || snapshot["retained"] != true || snapshot["contacts"] != 0 {
		t.Fatalf("wait dropped mail or escalated instead: %v %v", snapshot, err)
	}
	// A later authenticated read reaches the retained mail and removes the
	// obsolete wait label without any navigation or claim that it was read earlier.
	inbox, err := f.e.Inbox(f.ctx, f.recipient)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(inbox)
	if err != nil || !strings.Contains(string(raw), private) {
		t.Fatalf("retained mail unavailable on return: %v %v", inbox, err)
	}
	board, err = f.e.Board(f.ctx)
	if err != nil || board["app_binding_alerts"] != nil {
		t.Fatalf("fresh contact retained obsolete wait: %v %v", board["app_binding_alerts"], err)
	}
}
