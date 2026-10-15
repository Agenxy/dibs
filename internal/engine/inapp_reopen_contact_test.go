// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/logs"
)

type reopenContactFixture struct {
	e       *Engine
	ctx     context.Context
	cancel  context.CancelFunc
	ledger  *memLedger
	opened  chan []string
	sender  string
	created uint64
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
	oldRunning, oldSettle := claudeRunning, reopenSettle
	claudeRunning = func(string) bool { return false }
	reopenSettle = 20 * time.Millisecond
	t.Cleanup(func() { claudeRunning, reopenSettle = oldRunning, oldSettle })
	led := &memLedger{}
	e := New(core.NewState("reopen-contact", core.DefaultLimits()), led, deadProber{})
	e.SetHumanNotifier(noContactDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	stopWakeTimersOnCleanup(t, e)
	f := reopenContactFixture{e: e, ctx: ctx, cancel: cancel, ledger: led, opened: opened}
	worker := f.do(t, &core.Op{
		Kind: core.OpRegister, Name: "lead", Nonce: "reopen-contact-lead",
		SessionID: cli, AgentKind: core.KindPersistent,
		Agent: &core.AgentInfo{Harness: "Claude Code", Surface: harnessenv.ClaudeDesktop, CWD: t.TempDir()},
	})
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

func TestReopenedClaudeSessionWithNoProcessCreatesDurableContact(t *testing.T) {
	f := newReopenContactFixture(t)
	const private = "PRIVATE-REOPEN-CONTACT-MAIL"
	sent := f.do(t, &core.Op{
		Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: private,
	})
	serial, ok := sent["msg_serial"].(uint64)
	if !ok || serial == 0 {
		t.Fatalf("setup: send did not return a message: %v", sent)
	}
	select {
	case argv := <-f.opened:
		if len(argv) != 2 || argv[1] != "claude://code/continue?session=local_08697f65-6786-4c61-96f8-9af1aa07b24e" {
			t.Fatalf("setup: wrong app route: %v", argv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setup: actual send did not reach the app opener")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := f.e.query(f.ctx, func() core.Result {
			m := f.e.state.Messages[serial]
			if m == nil {
				return core.Result{"missing": true}
			}
			contact := f.e.state.Contacts[m.ContactEscalatedAt]
			if contact == nil {
				return core.Result{"contact": uint64(0)}
			}
			ledgered := false
			for _, op := range f.ledger.ops {
				if op.Kind == core.OpContactEscalate && op.MsgSerial == serial {
					ledgered = true
				}
			}
			notice, _ := f.e.contactNotice(contact)
			raw, marshalErr := json.Marshal(notice)
			return core.Result{
				"contact": contact.Serial, "source": contact.OldestSerial, "ledgered": ledgered,
				"notified": contact.NotifiedAt, "notice": string(raw), "notice_error": marshalErr, "state": m.State,
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot["contact"] != uint64(0) && snapshot["contact"] != nil {
			if snapshot["source"] != serial || snapshot["ledgered"] != true || snapshot["state"] != core.MsgStatePending {
				t.Fatalf("contact did not preserve its pending source and ledger op: %v", snapshot)
			}
			if !snapshot["notified"].(time.Time).IsZero() {
				t.Fatal("a durable contact is not a posted human notification")
			}
			if snapshot["notice_error"] != nil {
				t.Fatal("setup: contact notice could not be encoded:", snapshot["notice_error"])
			}
			if strings.Contains(snapshot["notice"].(string), private) {
				t.Fatal("contact notice leaked private mail")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("successful app open with no process never recorded contact escalation: %v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A late app-open result must refer to the incarnation that requested it.
// Both attempts start with real sends; only the old attempt is allowed to fail.
func TestLateReopenFailureCannotEscalateAReplacementRecipient(t *testing.T) {
	ring := logs.NewRing(128)
	previous := slog.Default()
	handler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(logs.NewHandler(handler, ring)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	f := newReopenContactFixture(t)
	oldRelease, newRelease := make(chan struct{}), make(chan struct{})
	opened := make(chan int32, 2)
	var count atomic.Int32
	var releaseOld, releaseNew sync.Once
	shower.Open = func([]string) error {
		n := count.Add(1)
		opened <- n
		if n == 1 {
			<-oldRelease
		} else {
			<-newRelease
		}
		return nil
	}
	t.Cleanup(func() {
		f.cancel()
		releaseOld.Do(func() { close(oldRelease) })
		releaseNew.Do(func() { close(newRelease) })
	})
	f.do(t, &core.Op{
		Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "old incarnation request",
	})
	select {
	case n := <-opened:
		if n != 1 {
			t.Fatalf("setup: first open=%d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setup: no old open")
	}
	removed, err := f.e.query(f.ctx, func() core.Result {
		old := f.e.state.Agents["lead"]
		archived := old.DormantSince.Add(f.e.state.Limits.DormancyMax + time.Second)
		for _, at := range []time.Time{archived, archived.Add(f.e.state.Limits.ArchiveRetention + time.Second)} {
			op := &core.Op{
				Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true, KeepArchivedNonce: true,
			}
			if _, err := f.e.applyAndLedger(op, at); err != nil {
				return core.Result{"error": err}
			}
		}
		return core.Result{"removed": f.e.state.Agents["lead"] == nil}
	})
	if err != nil || removed["removed"] != true {
		t.Fatalf("setup: real lifecycle did not release old identity: %v / %v", removed, err)
	}
	replacement := f.do(t, &core.Op{
		Kind: core.OpRegister, Name: "lead", Nonce: "reopen-contact-replacement",
		AgentKind: core.KindPersistent, SessionID: "6336088d-1111-4222-8333-444455556666",
		Agent: &core.AgentInfo{Harness: "Claude Code", Surface: harnessenv.ClaudeDesktop, CWD: t.TempDir()},
	})
	if replacement["agent_id"] != "lead" {
		t.Fatalf("setup: replacement did not reuse the id: %v", replacement)
	}
	f.do(t, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"lead"}})
	sent := f.do(t, &core.Op{
		Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "replacement request",
	})
	serial := sent["msg_serial"].(uint64)
	select {
	case n := <-opened:
		if n != 2 {
			t.Fatalf("setup: second open=%d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setup: replacement's real send did not open")
	}
	replacementState, err := f.e.query(f.ctx, func() core.Result {
		return core.Result{"created": f.e.state.Agents["lead"].CreatedSerial, "contacts": len(f.e.state.Contacts)}
	})
	if err != nil || replacementState["created"].(uint64) <= f.created || replacementState["contacts"] != 0 {
		t.Fatalf("setup: replacement was not a fresh, unalerted incarnation: %v / %v", replacementState, err)
	}
	releaseOld.Do(func() { close(oldRelease) })
	deadline := time.Now().Add(2 * time.Second)
	for {
		failed := false
		for _, record := range ring.Tail(0) {
			if record.Msg == "reopened this agent's Claude app session but no process for it appeared" &&
				record.Attrs["agent"] == "lead" {
				failed = true
			}
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: old reopen failure never reached its timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The replacement's opener stays blocked throughout this observation; any
	// contact for its fresh mail can only have come from the old callback.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		state, err := f.e.query(f.ctx, func() core.Result {
			m := f.e.state.Messages[serial]
			return core.Result{"contact": m.ContactEscalatedAt, "contacts": len(f.e.state.Contacts), "state": m.State}
		})
		if err != nil || state["contact"] != uint64(0) || state["contacts"] != 0 || state["state"] != core.MsgStatePending {
			t.Fatalf("old reopen failure touched replacement mail: %v / %v", state, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
