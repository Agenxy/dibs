// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Adoption must enter the destination's EXISTING arrival/contact path. Calling
// escalateContact by hand would miss a transfer that emits no arrival at all.
func TestAdoptedUnreadMailUsesOrdinaryDestinationContact(t *testing.T) {
	for _, kind := range []string{core.MsgRequest, core.MsgNotify} {
		t.Run(kind, func(t *testing.T) {
			led := &memLedger{}
			e, ctx := runContactReplayEngine(t, core.NewState("adopt-contact", core.DefaultLimits()), led, nil)
			tokens := map[string]string{}
			for _, id := range []string{"lead", "sender", "lost", "heir"} {
				tokens[id] = contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: id, Nonce: "adopt-contact-" + id, SessionID: "adopt-session-" + id, Agent: &core.AgentInfo{Harness: "terminal harness"}})["token"].(string)
				contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
			}
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator})
			mail := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSendMessage, Token: tokens["sender"], To: "lost", MsgType: kind, RequestPriority: "high", Body: "private mail"})["msg_serial"].(uint64)
			source := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpContactEscalate, MsgSerial: mail})["contact_serial"].(uint64)
			// Setup the same absent-route/no-live-turn premise as the existing
			// contact fixtures, on the writer, and assert the premise itself.
			_, err := e.query(ctx, func() core.Result {
				for _, id := range []string{"lost", "heir"} {
					l := e.state.Agents[id]
					l.Status, l.LastCoordination = core.StatusDormant, time.Now().Add(-contactActivityGrace-time.Minute)
					delete(e.seen, id)
					if !e.contactRouteMissingNow(l) || e.recentlyInTouch(l) || e.contactLooksLive(l) {
						t.Error("setup: recipient still reachable or live")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			events, unsubscribe := e.Subscribe(0)
			defer unsubscribe()
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAdoptAgent, Token: tokens["lead"], To: "lost", Space: "heir"})
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case ev := <-events:
					if ev.Type != "contact.escalated" || ev.Agent != "heir" {
						continue
					}
					_, err := e.query(ctx, func() core.Result {
						c := e.state.Contacts[ev.Serial]
						if c == nil || c.OldestSerial != mail || e.state.Messages[mail].ContactEscalatedAt != ev.Serial || e.state.Contacts[source].ResolvedAt.IsZero() {
							t.Error("contact transfer lost ownership or left source unresolved")
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					return
				case <-timer.C:
					t.Fatal("adopted unread mail never reached the destination contact path")
				}
			}
		})
	}
}
