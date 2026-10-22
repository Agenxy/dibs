// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestResolvedContactNoticeStopsBlockingHooksAndReplay(t *testing.T) {
	for _, resolution := range []string{"read", "clear", "unresolved"} {
		t.Run(resolution, func(t *testing.T) {
			led := &memLedger{}
			e, ctx := runContactReplayEngine(t, core.NewState("contact-restart", core.DefaultLimits()), led, nil)
			tokens := map[string]string{}
			for _, id := range []string{"lead", "sender", "unreachable"} {
				tokens[id] = contactReplayDo(t, e, ctx, &core.Op{
					Kind: core.OpRegister, Name: id, Nonce: "resolved-contact-" + id, SessionID: id + "-contact-session",
					Agent: &core.AgentInfo{Harness: "terminal harness"},
				})["token"].(string)
			}
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator})
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})
			if _, err := e.HookPoll(ctx, "lead-contact-session", "Stop", "", true, false); err != nil {
				t.Fatal("setup:", err)
			}
			if _, err := e.query(ctx, func() core.Result {
				l := e.state.Agents["unreachable"]
				l.Status, l.LastCoordination = core.StatusDormant, time.Now().Add(-contactActivityGrace-time.Minute)
				delete(e.seen, "unreachable")
				if !e.contactRouteMissingNow(l) || e.recentlyInTouch(l) || e.contactLooksLive(l) {
					t.Error("setup: recipient must have a confirmed missing route and no live-turn grace")
				}
				return nil
			}); err != nil {
				t.Fatal("setup:", err)
			}
			events, unsubscribe := e.Subscribe(0)
			defer unsubscribe()
			mail := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: tokens["sender"], To: "unreachable", MsgType: core.MsgRequest, Body: "private contact source",
			})["msg_serial"].(uint64)
			// Sends enter the production finite arrival batch before the route
			// decision. Wait for its result rather than injecting an escalation.
			escalation := waitResolvedContactEscalation(t, events)
			probe, err := e.query(ctx, func() core.Result {
				return core.Result{"contact": e.state.Messages[mail].ContactEscalatedAt, "news": fmt.Sprint(e.takeNotices("lead"))}
			})
			if err != nil {
				t.Fatal("setup:", err)
			}
			contact := probe["contact"].(uint64)
			needle := fmt.Sprintf("contact alert %d", contact)
			if contact == 0 || escalation.Serial != contact || escalation.Data["contact_serial"] != contact ||
				!strings.Contains(probe["news"].(string), needle) {
				t.Fatalf("setup: real send did not raise coordinator contact news: %v", probe)
			}
			if resolution == "unresolved" {
				before, err := e.HookPoll(ctx, "lead-contact-session", "Stop", "", false, false)
				if err != nil || before["decision"] != "block" || !strings.Contains(fmt.Sprint(before), needle) {
					t.Fatalf("setup: unresolved real alert did not reach Stop: %v %v", before, err)
				}
			}
			switch resolution {
			case "read":
				if _, err := e.Inbox(ctx, tokens["unreachable"]); err != nil {
					t.Fatal("setup recipient read:", err)
				}
			case "clear":
				contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpContactResolved, ContactSerial: contact})
			}
			state, err := e.query(ctx, func() core.Result {
				return core.Result{"resolved": !e.state.Contacts[contact].ResolvedAt.IsZero(), "news": fmt.Sprint(e.takeNotices("lead"))}
			})
			if err != nil || state["resolved"] != (resolution != "unresolved") {
				t.Fatalf("setup: wrong alert resolution: %v %v", state, err)
			}
			if got := strings.Contains(state["news"].(string), needle); got != (resolution == "unresolved") {
				t.Errorf("%s contact notice outstanding=%v: %v", resolution, got, state)
			}
			for _, phase := range []string{"live", "replay"} {
				current, currentCtx := e, ctx
				if phase == "replay" {
					current, currentCtx = replayContactEngine(t, e, ctx, led)
				}
				view, err := current.query(currentCtx, func() core.Result {
					return core.Result{
						"read":    current.state.Agents["lead"].ContactNoticeReadAt,
						"history": current.state.Contacts[contact] != nil,
						"news":    fmt.Sprint(current.takeNotices("lead")),
					}
				})
				if err != nil || view["read"] != uint64(0) || view["history"] != true {
					t.Fatalf("%s resolution forged a read or erased contact history: %v %v", phase, view, err)
				}
				if strings.Contains(view["news"].(string), needle) != (resolution == "unresolved") {
					t.Errorf("%s wrong outstanding contact notice: %v", phase, view)
				}
				for _, event := range []string{"Stop", "SessionStart"} {
					got, err := current.HookPoll(currentCtx, "lead-contact-session", event, "", false, false)
					if err != nil {
						t.Fatal(err)
					}
					if resolution != "unresolved" && (got["decision"] == "block" || strings.Contains(fmt.Sprint(got), needle)) {
						t.Errorf("%s %s still carries/blocks for %s contact: %v", phase, event, resolution, got)
					}
				}
			}
			if _, err := e.query(ctx, func() core.Result {
				e.pushNotice("lead", "unrelated coordination news", e.state.Serial+1, time.Now())
				cachedContact := false
				for _, n := range e.notices["lead"] {
					cachedContact = cachedContact || n.Kind == "contact.escalated"
				}
				if cachedContact != (resolution == "unresolved") ||
					!strings.Contains(fmt.Sprint(e.takeNotices("lead")), "unrelated coordination news") {
					t.Error("new notice retained obsolete protected contact or lost unrelated news")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func waitResolvedContactEscalation(t *testing.T, events <-chan core.Event) core.Event {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Type == "contact.escalated" && ev.Agent == "unreachable" {
				return ev
			}
		case <-deadline.C:
			t.Fatal("setup: actual send did not produce contact escalation")
		}
	}
}
