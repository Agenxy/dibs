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

// Enter through a real send and authenticated read before asking the same
// session's Stop hook whether the coordinator still owes this notice.
func TestContactNoticeReadStopsCoordinatorNag(t *testing.T) {
	for _, path := range []string{"inbox", "check_in", "inbox_page", "check_in_page"} {
		t.Run(path, func(t *testing.T) {
			led := &memLedger{}
			e, ctx := runContactReplayEngine(t, core.NewState("contact-restart", core.DefaultLimits()), led, nil)
			tokens := map[string]string{}
			for _, id := range []string{"lead", "sender", "unreachable"} {
				tokens[id] = contactReplayDo(t, e, ctx, &core.Op{
					Kind: core.OpRegister, Name: id, Nonce: "contact-stop-" + id,
					SessionID: id + "-contact-session", Agent: &core.AgentInfo{Harness: "terminal harness"},
				})["token"].(string)
			}
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator})
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})
			if _, err := e.HookPoll(ctx, "lead-contact-session", "Stop", "", true, false); err != nil {
				t.Fatal("setup Stop:", err)
			}
			if _, err := e.query(ctx, func() core.Result {
				l := e.state.Agents["unreachable"]
				l.Status, l.LastCoordination = core.StatusDormant, time.Now().Add(-contactActivityGrace-time.Minute)
				delete(e.seen, "unreachable")
				if !e.contactRouteMissingNow(l) || e.recentlyInTouch(l) || e.contactLooksLive(l) {
					t.Error("setup: recipient has a live route or grace")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			events, unsubscribe := e.Subscribe(0)
			defer unsubscribe()
			mail := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: tokens["sender"], To: "unreachable",
				MsgType: core.MsgRequest, Body: "unread source must remain pending",
			})["msg_serial"].(uint64)
			contact := waitResolvedContactEscalation(t, events).Serial
			needle := fmt.Sprintf("contact alert %d", contact)
			firstStop, err := e.HookPoll(ctx, "lead-contact-session", "Stop", "", false, false)
			if err != nil || firstStop["decision"] != "block" || !strings.Contains(fmt.Sprint(firstStop), needle) {
				t.Fatalf("setup: unread escalation does not reach Stop: %v %v", firstStop, err)
			}
			if got := readContactStopUpdates(t, e, ctx, tokens["lead"], path); !strings.Contains(got, needle) {
				t.Fatalf("setup: first authenticated read lacks contact: %s", got)
			}
			// Replay first also removes the first Stop's presentation throttle:
			// quiet must follow the durable read, not a recent nudge timestamp.
			for _, phase := range []string{"replay", "live"} {
				current, currentCtx := e, ctx
				if phase == "replay" {
					current, currentCtx = replayContactEngine(t, e, ctx, led)
				}
				assertContactStopQuiet(t, current, currentCtx, contact, mail)
			}
			if got := readContactStopUpdates(t, e, ctx, tokens["lead"], path); strings.Contains(got, needle) {
				t.Fatalf("second authenticated read repeated contact: %s", got)
			}
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpContactResolved, ContactSerial: contact})
			laterMail := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: tokens["sender"], To: "unreachable",
				MsgType: core.MsgRequest, Body: "later source for the same unreachable agent",
			})["msg_serial"].(uint64)
			laterContact := waitResolvedContactEscalation(t, events).Serial
			if laterContact <= contact {
				t.Fatalf("setup: later same-recipient escalation reused resolved contact: %d", laterContact)
			}
			laterNeedle := fmt.Sprintf("contact alert %d", laterContact)
			if got := readContactStopUpdates(t, e, ctx, tokens["lead"], path); !strings.Contains(got, laterNeedle) || strings.Contains(got, needle) {
				t.Fatalf("later same-recipient escalation lost or repeated old news: %s", got)
			}
			assertContactStopQuiet(t, e, ctx, laterContact, laterMail)
			question := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: tokens["lead"], To: "sender",
				MsgType: core.MsgQuestion, Body: "unrelated question",
			})["msg_serial"].(uint64)
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRespond, Token: tokens["sender"],
				MsgSerial: question, Disposition: "answer", Body: "unrelated answer must still wake"})
			got, err := e.HookPoll(ctx, "lead-contact-session", "Stop", "", false, false)
			if err != nil || got["decision"] != "block" || !strings.Contains(fmt.Sprint(got), "unrelated answer must still wake") {
				t.Fatalf("unrelated blocking outcome no longer reaches Stop: %v %v", got, err)
			}
		})
	}
}

func readContactStopUpdates(t *testing.T, e *Engine, ctx context.Context, token, path string) string {
	t.Helper()
	if path == "inbox" || path == "check_in" {
		return readContactUpdates(t, e, ctx, token, path)
	}
	var r core.Result
	var err error
	if path == "inbox_page" {
		r, err = e.InboxPage(ctx, token, "", 1)
	} else {
		r, err = e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token, MailboxPage: true, MailboxLimit: 1})
	}
	if err != nil {
		t.Fatal("authenticated page:", err)
	}
	return fmt.Sprint(r["agent_updates"])
}

func assertContactStopQuiet(t *testing.T, e *Engine, ctx context.Context, contact, mail uint64) {
	t.Helper()
	got, err := e.HookPoll(ctx, "lead-contact-session", "Stop", "", false, false)
	if err != nil || got["decision"] == "block" || strings.Contains(fmt.Sprint(got), "contact alert") {
		t.Fatalf("Stop after authenticated read repeated contact: %v %v", got, err)
	}
	view, err := e.query(ctx, func() core.Result {
		return core.Result{"blocking": e.blockingNotices("lead"), "alerts": e.state.Board()["contact_alerts"],
			"resolved": !e.state.Contacts[contact].ResolvedAt.IsZero(), "mail": e.state.Messages[mail].State}
	})
	if err != nil {
		t.Fatal(err)
	}
	alerts, _ := view["alerts"].([]*core.ContactEscalation)
	if view["blocking"] != 0 || len(alerts) != 1 || view["resolved"] != false || view["mail"] != core.MsgStatePending {
		t.Fatalf("read retained blocking news or settled human contact/source: %v", view)
	}
}
