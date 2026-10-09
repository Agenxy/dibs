// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/notify"
)

type contactReadDesktop struct {
	receipt bool
	posted  chan humanask.Message
}

func (contactReadDesktop) Available() bool                   { return true }
func (contactReadDesktop) Presentation() notify.Presentation { return notify.Presentation{} }
func (contactReadDesktop) RemoveMessages(string, []uint64) notify.Cleanup {
	return notify.Cleanup{State: "requested", BestEffort: true}
}

func (n contactReadDesktop) Ask(m humanask.Message) (humanask.Answer, error) {
	if n.receipt {
		m.Receipt("posted")
	}
	n.posted <- m
	return humanask.Answer{}, nil
}

// A coordinator reading a reachability alert consumes its notification, not
// the unresolved contact on the board or the person's independent delivery.
// Enter through the writer's real contact op and authenticated MCP reads;
// hand-inserting a notice would miss its message and event classifications.
func TestContactAlertReadQuietsCoordinatorWithoutResolvingContact(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, read := range []string{"check_in", "inbox"} {
			for _, delivery := range []string{"unposted", "posted"} {
				for _, activation := range []string{"cold", "hooked"} {
					t.Run(version+"/"+read+"/"+delivery+"/"+activation, func(t *testing.T) {
						srv, eng, _ := restartableQueueServer(t, t.TempDir())
						posted := make(chan humanask.Message, 1)
						eng.SetHumanNotifier(contactReadDesktop{delivery == "posted", posted})
						call := func(name string, args map[string]any) map[string]any {
							t.Helper()
							r := toolCallOn(t, srv, version, name, args)
							if r["error"] != nil || r["__is_error"] == true {
								t.Fatalf("setup %s: %v", name, r)
							}
							return r
						}
						register := func(name string) string {
							t.Helper()
							r := call("register", map[string]any{"name": name, "nonce": "contact-read-" + name, "session_id": "contact-read-" + name})
							token, ok := r["token"].(string)
							if !ok || token == "" {
								t.Fatalf("setup: registration failed: %v", r)
							}
							call("check_in", map[string]any{"token": token})
							return token
						}
						lead := register("lead")
						sender := register("sender")
						recipient := register("unreachable")
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						if _, err := eng.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator}); err != nil {
							t.Fatal("setup: coordinator grant:", err)
						}
						call("check_in", map[string]any{"token": lead})
						sent := call("send", map[string]any{"token": sender, "to": "unreachable", "type": "request", "body": "private unread source"})
						serial, ok := sent["msg_serial"].(float64)
						if !ok || serial == 0 {
							t.Fatalf("setup: send failed: %v", sent)
						}
						r, err := eng.Do(ctx, &core.Op{Kind: core.OpContactEscalate, MsgSerial: uint64(serial)})
						if err != nil || r["coalesced"] != false {
							t.Fatalf("setup: native contact op did not publish a new alert: %v %v", r, err)
						}
						contact, ok := r["contact_serial"].(uint64)
						if !ok || contact == 0 {
							t.Fatal("setup: missing contact identity")
						}
						select {
						case notice := <-posted:
							if notice.Type != "contact" || notice.Serial != contact || notice.Contact == nil ||
								notice.Contact.Message != uint64(serial) || strings.Contains(notice.Body, "private unread source") {
								t.Fatalf("setup: person's route lost or exposed contact metadata: %+v", notice)
							}
						case <-ctx.Done():
							t.Fatal("setup: no contact delivered through the person's notifier fixture")
						}
						marker := fmt.Sprintf("contact alert %d", contact)
						if activation == "hooked" {
							wake := call("hook_poll", map[string]any{"session_id": "contact-read-lead", "event": "Stop", "strict_output": true})
							if !strings.Contains(fmt.Sprint(wake), marker) {
								t.Fatalf("setup: token-less Stop did not present the outstanding contact: %v", wake)
							}
						}
						first := call(read, map[string]any{"token": lead})
						if !strings.Contains(fmt.Sprint(first["agent_updates"]), marker) {
							t.Fatalf("setup: authenticated first read did not deliver the contact alert: %v", first)
						}
						second := call(read, map[string]any{"token": lead})
						if strings.Contains(fmt.Sprint(second["agent_updates"]), marker) {
							t.Errorf("already read contact alert repeated through %s: %v", read, second)
						}
						stop := call("hook_poll", map[string]any{"session_id": "contact-read-lead", "event": "Stop", "strict_output": true})
						if stop["decision"] == "block" || strings.Contains(fmt.Sprint(stop), marker) {
							t.Errorf("read contact alert extended the coordinator's Stop: %v", stop)
						}
						checkpoint := call("check_in", map[string]any{"token": lead})
						if strings.Contains(fmt.Sprint(checkpoint["agent_updates"]), marker) {
							t.Errorf("read contact alert repeated through the next checkpoint: %v", checkpoint)
						}
						board, err := eng.Board(ctx)
						if err != nil {
							t.Fatal(err)
						}
						b, err := json.Marshal(board["contact_alerts"])
						if err != nil {
							t.Fatal(err)
						}
						var alerts []map[string]any
						if err := json.Unmarshal(b, &alerts); err != nil || len(alerts) != 1 ||
							alerts[0]["serial"] != float64(contact) || alerts[0]["resolved_at"] != nil || (alerts[0]["notified_at"] != nil) != (delivery == "posted") {
							t.Fatalf("coordinator read changed the unresolved posted board alert: %s (%v)", b, err)
						}
						call("inbox", map[string]any{"token": recipient})
						resolved, err := eng.Board(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if resolved["contact_alerts"] != nil {
							t.Fatalf("recipient's actual read left a resolved alert on the board: %v", resolved["contact_alerts"])
						}
					})
				}
			}
		}
	}
}
