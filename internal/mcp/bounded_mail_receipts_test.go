// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

func mailboxReceipt(t *testing.T, eng *engine.Engine, id uint64) core.Message {
	t.Helper()
	r, err := eng.AllMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r["messages"].([]*core.Message) {
		if m.Serial == id {
			return *m
		}
	}
	t.Fatalf("setup: message %d missing", id)
	return core.Message{}
}

func TestCompleteFYIReadAndHookNeedNoAck(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, route := range []string{"read_mail", "Stop"} {
			t.Run(version+"/"+route, func(t *testing.T) {
				srv, _ := newServer(t)
				eng := srv.Config.Handler.(*Server).eng
				reader := toolCall(t, srv, "register", map[string]any{"name": "reader", "session_id": "reader-session"})
				sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})
				sent := waitingCall(t, srv, version, "send", map[string]any{"token": sender["token"], "to": "reader", "type": "notify", "body": "complete-fyi-tail"})
				id := uint64(sent["msg_serial"].(float64))
				waitingCall(t, srv, version, "read_mail", map[string]any{"token": sender["token"], "msg_serial": id})
				if mailboxReceipt(t, eng, id).Consumed {
					t.Fatal("sender reading consumed recipient mail")
				}
				var out map[string]any
				if route == "Stop" {
					out = waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "Stop"})
				} else {
					out = waitingCall(t, srv, version, "read_mail", map[string]any{"token": reader["token"], "msg_serial": id})
				}
				if !strings.Contains(fmt.Sprint(out), "complete-fyi-tail") || !mailboxReceipt(t, eng, id).Consumed {
					t.Fatalf("full %s presentation did not consume FYI: %v", route, out)
				}
				again := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "UserPromptSubmit"})
				if strings.Contains(fmt.Sprint(again), "complete-fyi-tail") || again["decision"] == "block" {
					t.Fatal("complete FYI repeated")
				}
			})
		}
	}
}

func TestCompactMailboxDeliversOnlyItsPageAndRetainsFullBodies(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, method := range []string{"inbox", "check_in"} {
			t.Run(version+"/"+method, func(t *testing.T) {
				srv, _, eng := boundedMailServer(t)
				ids := []uint64{}
				body := strings.Repeat("retained content ", 450) + "FULL-ENVELOPE-TAIL"
				for range 10 {
					r := waitingCall(t, srv, version, "send", map[string]any{"token": "sender-token", "to": "reader", "type": "notify", "body": body})
					ids = append(ids, uint64(r["msg_serial"].(float64)))
				}
				page := waitingCall(t, srv, version, method, map[string]any{"token": "reader-token", "limit": 3})
				items := page["inbox"].([]any)
				if len(items) != 3 || uint64(items[2].(map[string]any)["serial"].(float64)) != ids[0] {
					t.Fatalf("actionable-first page: %v", page)
				}
				if strings.Contains(fmt.Sprint(items), "FULL-ENVELOPE-TAIL") {
					t.Fatal("full body escaped compact page")
				}
				for i, id := range ids {
					m := mailboxReceipt(t, eng, id)
					if i == 0 {
						if m.State != core.MsgStateDelivered || m.DeliveredAt == 0 {
							t.Fatal("returned envelope lacks its delivery receipt")
						}
					} else if m.State != core.MsgStatePending || m.DeliveredAt != 0 {
						t.Fatalf("omitted %d was delivered", id)
					}
					if m.Body != body || m.Consumed {
						t.Fatal("page changed retained content or acknowledged mail")
					}
				}
				full := waitingCall(t, srv, version, "read_mail", map[string]any{"token": "reader-token", "msg_serial": ids[0]})
				if !strings.Contains(fmt.Sprint(full), "FULL-ENVELOPE-TAIL") {
					t.Fatal("read_mail lost omitted content")
				}
				cursor, _ := page["next_cursor"].(string)
				if cursor == "" {
					t.Fatal("omitted page has no continuation")
				}
				next := waitingCall(t, srv, version, "inbox", map[string]any{"token": "reader-token", "cursor": cursor, "limit": 3})
				if next["inbox"].([]any)[0].(map[string]any)["serial"] != float64(ids[1]) {
					t.Fatal("delivery receipt changed traversal priority")
				}
			})
		}
	}
}

func TestPassiveMailboxCountsUnitsAndNeverReadsAnOutcomePrefix(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, eng := boundedMailServer(t)
			r := waitingCall(t, srv, version, "send", map[string]any{"token": "reader-token", "to": "sender", "type": "request", "body": "work"})
			id := uint64(r["msg_serial"].(float64))
			waitingCall(t, srv, version, "respond", map[string]any{"token": "sender-token", "msg_serial": id, "disposition": "approve", "body": "accepted"})
			for i := range 17 {
				waitingCall(t, srv, version, "respond", map[string]any{"token": "sender-token", "msg_serial": id, "disposition": "progress", "body": fmt.Sprintf("unit %d %s", i, strings.Repeat("omitted report ", 100))})
			}
			before := mailboxReceipt(t, eng, id).OutcomeReadAt
			for range 2 {
				got := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "UserPromptSubmit"})
				text := fmt.Sprint(got["hookSpecificOutput"])
				if !strings.Contains(text, "18 outstanding update units") || !strings.Contains(text, "announced FYIs awaiting presentation") {
					t.Fatalf("rendered lines substituted for outstanding units: %v", got)
				}
				if mailboxReceipt(t, eng, id).OutcomeReadAt != before {
					t.Fatal("passive pointer advanced an unread outcome prefix")
				}
			}
			got := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "UserPromptSubmit"})
			if !strings.Contains(fmt.Sprint(got), "18 outstanding update units") || mailboxReceipt(t, eng, id).OutcomeReadAt != before {
				t.Fatal("FYI presentation changed unrelated outcome state")
			}
		})
	}
}

func TestBoundedMailboxRetainsOmittedProjectionCounts(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, method := range []string{"inbox", "check_in"} {
			t.Run(version+"/"+method, func(t *testing.T) {
				srv, _, _ := boundedMailServer(t)
				page := waitingCall(t, srv, version, method, map[string]any{"token": "reader-token", "limit": 1})
				if len(page["owed_work"].([]any)) != 1 || page["more_owed_work"] != float64(1) {
					t.Fatalf("omitted owed projection lost its count: %v", page)
				}
			})
		}
	}
}
