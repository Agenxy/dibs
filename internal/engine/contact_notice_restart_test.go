// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func runContactReplayEngine(t *testing.T, st *core.State, led *memLedger, history []core.Event) (*Engine, context.Context) {
	t.Helper()
	e := New(st, led, deadProber{}, history)
	e.SetHumanNotifier(noContactDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { e.Run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })
	return e, ctx
}

func contactReplayDo(t *testing.T, e *Engine, ctx context.Context, op *core.Op) core.Result {
	t.Helper()
	r, err := e.Do(ctx, op)
	if err != nil {
		t.Fatalf("setup %s: %v", op.Kind, err)
	}
	return r
}

func replayContactEngine(t *testing.T, e *Engine, ctx context.Context, led *memLedger) (*Engine, context.Context) {
	t.Helper()
	var ops []*core.Op
	if _, err := e.query(ctx, func() core.Result {
		ops = append(ops, led.ops...)
		return nil
	}); err != nil {
		t.Fatal("snapshot ledger:", err)
	}
	st := core.NewState("contact-restart", core.DefaultLimits())
	var history []core.Event
	base := time.Now().Add(-time.Second)
	for i, op := range ops {
		_, events, err := st.Apply(op, base.Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			t.Fatalf("replay %d (%s): %v", i, op.Kind, err)
		}
		history = append(history, events...)
	}
	return runContactReplayEngine(t, st, &memLedger{}, history)
}

func readContactUpdates(t *testing.T, e *Engine, ctx context.Context, token, path string) string {
	t.Helper()
	var r core.Result
	var err error
	if path == "check_in" {
		r, err = e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token})
	} else {
		r, err = e.Inbox(ctx, token)
	}
	if err != nil {
		t.Fatal("read:", err)
	}
	return fmt.Sprint(r["agent_updates"])
}

func contactLedgerSize(t *testing.T, e *Engine, ctx context.Context, led *memLedger) int {
	t.Helper()
	r, err := e.query(ctx, func() core.Result { return core.Result{"count": len(led.ops)} })
	if err != nil {
		t.Fatal(err)
	}
	return r["count"].(int)
}

func TestContactNoticeRebuildSkipsPrunedRecipient(t *testing.T) {
	e := New(core.NewState("pruned-contact", core.DefaultLimits()), &memLedger{}, deadProber{}, []core.Event{{
		Type: "contact.escalated", To: "pruned", Agent: "unreachable", Serial: 10,
		Data: map[string]any{"contact_serial": uint64(10)},
	}})
	if notices := e.takeNotices("pruned"); len(notices) != 0 {
		t.Fatalf("pruned recipient retained contact notices: %+v", notices)
	}
}

func assertLaterContactStillUnread(t *testing.T, e *Engine, ctx context.Context, led *memLedger, tokens map[string]string, old string) {
	t.Helper()
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: "later-unreachable"})
	mail := contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSendMessage, Token: tokens["sender"],
		To: "later-unreachable", MsgType: core.MsgRequest, Body: "new unread source",
	})["msg_serial"].(uint64)
	contact := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpContactEscalate, MsgSerial: mail})["contact_serial"].(uint64)
	rebuilt, replayCtx := replayContactEngine(t, e, ctx, led)
	got := readContactUpdates(t, rebuilt, replayCtx, tokens["lead"], "inbox")
	if !strings.Contains(got, fmt.Sprintf("contact alert %d", contact)) || strings.Contains(got, old) {
		t.Fatalf("durable prefix lost later unread contact or repeated read contact: %s", got)
	}
}

// Both authenticated doors must write evidence, not merely delete a cache.
// Reconstruct state AND the notice event ring from actual ledger ops. The
// unread-restart control excludes a rebuild that suppresses every contact.
func TestContactNoticeReadSurvivesLedgerReplayAndActivation(t *testing.T) {
	for _, path := range []string{"inbox", "check_in"} {
		t.Run(path, func(t *testing.T) {
			led := &memLedger{}
			e, ctx := runContactReplayEngine(t, core.NewState("contact-restart", core.DefaultLimits()), led, nil)
			tokens := map[string]string{}
			for _, name := range []string{"lead", "sender", "unreachable"} {
				tokens[name] = contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: name, Nonce: "nonce-" + name})["token"].(string)
				contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAckBoard, Token: tokens[name]})
			}
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator})
			mail := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: tokens["sender"], To: "unreachable",
				MsgType: core.MsgRequest, Body: "source remains unread",
			})["msg_serial"].(uint64)
			contact := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpContactEscalate, MsgSerial: mail})["contact_serial"].(uint64)
			needle := fmt.Sprintf("contact alert %d", contact)
			before, beforeCtx := replayContactEngine(t, e, ctx, led)
			if got := readContactUpdates(t, before, beforeCtx, tokens["lead"], "inbox"); !strings.Contains(got, needle) {
				t.Fatalf("setup: unread notice lost on restart: %s", got)
			}
			if got := readContactUpdates(t, e, ctx, tokens["lead"], path); !strings.Contains(got, needle) {
				t.Fatalf("setup: first authenticated read lacks contact notice: %s", got)
			}
			written := contactLedgerSize(t, e, ctx, led)
			if got := readContactUpdates(t, e, ctx, tokens["lead"], "inbox"); strings.Contains(got, needle) {
				t.Fatalf("second live read repeated contact: %s", got)
			}
			if contactLedgerSize(t, e, ctx, led) != written {
				t.Fatal("read without new contact notice wrote another op")
			}
			// A credential-rotating activation clears AckedSerial. Read evidence
			// must not depend on that gate retaining its previous value.
			tokens["lead"] = contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpResume, Nonce: "nonce-lead", ResumeID: "contact-restart-activation"})["token"].(string)
			after, afterCtx := replayContactEngine(t, e, ctx, led)
			if got := readContactUpdates(t, after, afterCtx, tokens["lead"], "inbox"); strings.Contains(got, needle) {
				t.Errorf("consumed contact notice returned after ledger replay: %s", got)
			}
			r, err := after.query(afterCtx, func() core.Result {
				c := after.state.Contacts[contact]
				return core.Result{"board": after.state.Board()["contact_alerts"], "resolved": !c.ResolvedAt.IsZero(), "mail": after.state.Messages[mail].State}
			})
			alerts, _ := r["board"].([]*core.ContactEscalation)
			if err != nil || len(alerts) != 1 || r["resolved"] != false || r["mail"] != core.MsgStatePending {
				t.Fatalf("reading coordinator news settled source mail/contact or removed board alert: %+v %v", r, err)
			}
			assertLaterContactStillUnread(t, e, ctx, led, tokens, needle)
		})
	}
}
