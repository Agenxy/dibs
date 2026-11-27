// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

const approvalTestConversation = "01a10520-0000-7000-8000-000000000097"

func TestAntigravityContactReportsCommandApproval(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sqlite3"); os.IsNotExist(err) {
		t.Skip("system sqlite3 is unavailable")
	}
	for _, tc := range []struct {
		name, steps, reason string
		hotJournal          bool
	}{
		{"waiting-with-pending-tail", "(1,132,9),(2,132,1)", "antigravity_command_approval", false},
		{"command-completed", "(1,132,9),(2,132,3)", "", false},
		{"non-command-waiting", "(1,132,9),(2,15,9)", "", false},
		{"ambiguous-journal", "(1,132,9)", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeAntigravityApprovalConversation(t, tc.steps, tc.hotJournal)
			testAntigravityApprovalContact(t, tc.reason)
		})
	}
}

func writeAntigravityApprovalConversation(t *testing.T, steps string, hotJournal bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sql := "CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER); " +
		"INSERT INTO steps VALUES " + steps
	db := filepath.Join(dbDir, approvalTestConversation+".db")
	cmd := exec.CommandContext(t.Context(), "/usr/bin/sqlite3", "-init", os.DevNull, db, sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup conversation database: %v %s", err, out)
	}
	if hotJournal {
		if err := os.WriteFile(db+"-wal", []byte("active journal placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func testAntigravityApprovalContact(t *testing.T, reason string) {
	t.Helper()
	accepted := make(chan struct{}, 2)
	originalPrepare := prepareAntigravity
	prepareAntigravity = func(context.Context, string, string) (func(context.Context, string) error, error) {
		return func(context.Context, string) error { accepted <- struct{}{}; return nil }, nil
	}
	t.Cleanup(func() { prepareAntigravity = originalPrepare })
	e := New(core.NewState("approval-contact", core.DefaultLimits()), &memLedger{}, deadProber{})
	posted := make(chan humanask.Message, 1)
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
	if grant, err := e.GrantRole(ctx, "sender", core.RoleCoordinator); err != nil || grant["error"] != nil {
		t.Fatalf("setup coordinator: %v / %v", grant, err)
	}
	sleepAntigravitySendRecipient(t, e, ctx)
	send := func(body string) uint64 {
		t.Helper()
		serial := contactReplayDo(t, e, ctx, &core.Op{
			Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgRequest,
			RequestPriority: "high", Body: body,
		})["msg_serial"].(uint64)
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("setup: Antigravity did not accept the notice")
		}
		waitWakeDone(t, e, "worker")
		return serial
	}
	first := send("private first request")
	send("private second request")
	select {
	case m := <-posted:
		if m.Contact == nil || m.Contact.Message != first || m.Body != "" ||
			!strings.Contains(m.Contact.OpenHint, approvalTestConversation) {
			t.Fatal("posted contact did not identify the original unread request")
		}
		got := strings.Contains(m.Contact.OpenHint, "waiting for your approval of a command in Antigravity")
		if got != (reason != "") {
			t.Errorf("approval hint=%v, want %v: %s", got, reason != "", m.Contact.OpenHint)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("contact notice was not posted")
	}
	diagnostic, err := e.DiagnoseWake(ctx, sender, "worker")
	escalation, _ := diagnostic["contact_escalation"].(core.Result)
	if err != nil || escalation["msg_serial"] != first || escalation["reason"] != core.ContactReason(reason) {
		t.Errorf("diagnose_wake did not record the observed cause %q: %v / %v", reason, diagnostic, err)
	}
}
