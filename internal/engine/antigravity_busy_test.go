// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/procexec"
)

func TestAntigravityBusyTurnDefersContact(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sqlite3"); os.IsNotExist(err) {
		t.Skip("system sqlite3 is unavailable")
	}
	const generatingAfterTools = "(1722,132,3),(1723,132,3),(1724,15,8)"
	const generatingAfterNotice = "(1948,101,3),(1949,15,8)"
	for _, tc := range []struct {
		name  string
		steps string
		wal   bool
		read  bool
	}{
		{"generating", generatingAfterTools, false, false},
		{"generating-after-notice", generatingAfterNotice, false, false},
		{"generating-after-notice-with-live-wal", generatingAfterNotice, true, false},
		{"read-before-completion", generatingAfterTools, false, true},
		{"running", "(1724,15,2)", false, false},
		{"queued", "(1724,15,11)", false, false},
		{"recent-wal", "(1724,15,3)", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := writeAntigravityApprovalConversation(t, tc.steps, false)
			if tc.wal {
				if err := os.WriteFile(db+"-wal", []byte("recent journal placeholder"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			testAntigravityBusyTurn(t, db, tc.wal, tc.read)
		})
	}
}

type busyAntigravityFixture struct {
	e              *Engine
	ctx            context.Context
	worker, sender string
	accepted       chan struct{}
	posted         chan humanask.Message
}

func newBusyAntigravityFixture(t *testing.T) busyAntigravityFixture {
	t.Helper()
	accepted := make(chan struct{}, 2)
	previous := prepareAntigravity
	prepareAntigravity = func(context.Context, string, string) (func(context.Context, string) error, error) {
		return func(context.Context, string) error { accepted <- struct{}{}; return nil }, nil
	}
	t.Cleanup(func() { prepareAntigravity = previous })
	e := New(core.NewState("busy-contact", core.DefaultLimits()), &memLedger{}, deadProber{})
	posted := make(chan humanask.Message, 2)
	e.SetHumanNotifier(firstContactDesktop{detailed: true, posted: posted})
	ctx, cancel := context.WithCancel(t.Context())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.Done() })
	worker := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", AgentKind: core.KindPersistent, SessionAlias: approvalTestConversation,
		Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
	})["token"].(string)
	e.BindAntigravity(ctx, worker, approvalTestConversation)
	sender := registerAgent(t, e, "sender")
	if r, err := e.GrantRole(ctx, "sender", core.RoleCoordinator); err != nil || r["error"] != nil {
		t.Fatalf("setup coordinator: %v / %v", r, err)
	}
	sleepAntigravitySendRecipient(t, e, ctx)
	return busyAntigravityFixture{e, ctx, worker, sender, accepted, posted}
}

func (f busyAntigravityFixture) send(t *testing.T, body string) uint64 {
	t.Helper()
	r := contactReplayDo(t, f.e, f.ctx, &core.Op{
		Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgRequest,
		RequestPriority: "high", Body: body,
	})
	select {
	case <-f.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: wake was not accepted")
	}
	waitWakeDone(t, f.e, "worker")
	return r["msg_serial"].(uint64)
}

func testAntigravityBusyTurn(t *testing.T, db string, wal, read bool) {
	t.Helper()
	f := newBusyAntigravityFixture(t)
	first := f.send(t, "first")
	f.send(t, "second")
	deferral := f.requireDeferral(t)
	f.recheck(t, deferral)
	waitWakeDone(t, f.e, "worker")
	deferral = f.requireDeferral(t)
	if read {
		if inbox, err := f.e.Inbox(f.ctx, f.worker); err != nil || inbox["error"] != nil {
			t.Fatalf("read both requests: %v / %v", inbox, err)
		}
	}
	if wal {
		deferral = f.recheckStaleJournal(t, db, deferral)
	}
	finishBusyAntigravityTurn(t, f.ctx, db, wal)
	f.recheck(t, deferral)
	if read {
		diag, err := f.e.DiagnoseWake(f.ctx, f.sender, "worker")
		if err != nil || diag["contact_escalation"] != nil || diag["contact_deferred"] != nil {
			t.Fatalf("a read should settle the deferral: %v / %v", diag, err)
		}
		return
	}
	select {
	case m := <-f.posted:
		if m.Contact == nil || m.Contact.Message != first {
			t.Fatal("contact after turn completion omitted the first unread request")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed turn left unread mail without a contact alert")
	}
	select {
	case <-f.accepted:
		t.Fatal("the contact recheck submitted an extra wake")
	default:
	}
}

func (f busyAntigravityFixture) recheck(t *testing.T, deferral core.Result) {
	t.Helper()
	next, ok := deferral["next_check_at"].(time.Time)
	if !ok || next.IsZero() {
		t.Fatal("deferred contact has no bounded recheck")
	}
	if _, err := f.e.query(f.ctx, func() core.Result { f.e.sweep(next.Add(time.Millisecond)); return nil }); err != nil {
		t.Fatal(err)
	}
}

func (f busyAntigravityFixture) recheckStaleJournal(t *testing.T, db string, deferral core.Result) core.Result {
	t.Helper()
	stale := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(db+"-wal", stale, stale); err != nil {
		t.Fatal(err)
	}
	f.recheck(t, deferral)
	waitWakeDone(t, f.e, "worker")
	return f.requireDeferral(t)
}

func (f busyAntigravityFixture) requireDeferral(t *testing.T) core.Result {
	t.Helper()
	diag, err := f.e.DiagnoseWake(f.ctx, f.sender, "worker")
	deferral, _ := diag["contact_deferred"].(core.Result)
	if err != nil || deferral["reason"] != "turn in progress" || diag["contact_escalation"] != nil {
		t.Fatalf("the turn has not been observed to end and should defer contact: %v / %v", diag, err)
	}
	select {
	case <-f.posted:
		t.Fatal("human notification posted before observed completion")
	default:
	}
	return deferral
}

func finishBusyAntigravityTurn(t *testing.T, ctx context.Context, db string, wal bool) {
	t.Helper()
	if wal {
		if err := os.Remove(db + "-wal"); err != nil {
			t.Fatal(err)
		}
	}
	cmd := procexec.Command(ctx, "/usr/bin/sqlite3", "-init", os.DevNull, db,
		"UPDATE steps SET status=3 WHERE idx=(SELECT max(idx) FROM steps)")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("finish fixture turn: %v %s", err, out)
	}
}
