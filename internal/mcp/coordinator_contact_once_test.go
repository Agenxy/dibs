// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A contact notice is a separate delivery obligation even when the coordinator
// also sent its source request. Drive the real MCP Stop and encrypted replay;
// never populate the notice cache or its read watermark by hand.
func TestCoordinatorContactPresentationIsOneShot(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, version := range []string{"2026-07-28", "2025-11-25"} {
			for _, ownSource := range []bool{false, true} {
				for _, first := range []string{"Stop", "SubagentStop", "inbox", "check_in", "SessionStart", "held-Stop"} {
					t.Run(fmt.Sprintf("%s/replay=%t/sender=%t/%s", version, replay, ownSource, first), func(t *testing.T) {
						dir := t.TempDir()
						srv, eng, stop := aliasReplayServer(t, dir)
						call := func(name string, args map[string]any) map[string]any {
							t.Helper()
							return waitingCall(t, srv, version, name, args)
						}
						register := func(name string) string {
							t.Helper()
							r := call("register", map[string]any{
								"name": name, "nonce": "contact-once-" + name,
								"session_id": "contact-once-" + name, "kind": "persistent",
							})
							token, ok := r["token"].(string)
							if !ok || token == "" {
								t.Fatalf("setup: registration failed: %v", r)
							}
							call("check_in", map[string]any{"token": token})
							return token
						}
						lead, sender := register("lead"), register("sender")
						register("unreachable")
						if _, err := eng.Do(t.Context(), &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator}); err != nil {
							t.Fatal("setup coordinator:", err)
						}
						call("check_in", map[string]any{"token": lead})
						if ownSource {
							sender = lead
						}
						sent := call("send", map[string]any{"token": sender, "to": "unreachable", "type": "request", "body": "PRIVATE-UNREAD-CONTACT-SOURCE"})
						mail, ok := sent["msg_serial"].(float64)
						if !ok || mail == 0 {
							t.Fatalf("setup send: %v", sent)
						}
						r, err := eng.Do(t.Context(), &core.Op{Kind: core.OpContactEscalate, MsgSerial: uint64(mail)})
						if err != nil || r["coalesced"] != false {
							t.Fatalf("setup contact: %v %v", r, err)
						}
						contact := r["contact_serial"].(uint64)
						marker := fmt.Sprintf("contact alert %d ", contact)
						var presented map[string]any
						if first == "inbox" || first == "check_in" {
							presented = call(first, map[string]any{"token": lead})
						} else {
							event := first
							if event == "held-Stop" {
								held := call("hook_poll", map[string]any{"session_id": "contact-once-lead", "event": "Stop", "stop_hook_active": true, "strict_output": true})
								if held["decision"] == "block" || strings.Contains(fmt.Sprint(held), marker) {
									t.Fatalf("held Stop presented the notice: %v", held)
								}
								event = "Stop"
							}
							presented = call("hook_poll", map[string]any{"session_id": "contact-once-lead", "event": event, "strict_output": true})
						}
						if !strings.Contains(fmt.Sprint(presented), marker) {
							t.Fatalf("first presentation lost the known contact: %v", presented)
						}
						if strings.Contains(fmt.Sprint(presented), "PRIVATE-UNREAD-CONTACT-SOURCE") {
							t.Fatal("coordinator alert exposed the recipient's source body")
						}
						// Rebuild before any second pull: a timer or cache deletion is
						// insufficient. The encrypted ledger must retain presentation.
						if replay {
							stop()
							srv, eng, _ = aliasReplayServer(t, dir)
						}
						for _, read := range []string{"inbox", "check_in"} {
							if got := call(read, map[string]any{"token": lead}); strings.Contains(fmt.Sprint(got), marker) {
								t.Fatalf("%s repeated an already presented contact after replay: %v", read, got)
							}
						}
						got := call("hook_poll", map[string]any{"session_id": "contact-once-lead", "event": "Stop", "strict_output": true})
						if got["decision"] == "block" || strings.Contains(fmt.Sprint(got), marker) {
							t.Fatalf("Stop re-raised an already presented contact after replay: %v", got)
						}
						board, err := eng.Board(t.Context())
						if err != nil {
							t.Fatalf("contact board: %v %v", board, err)
						}
						encoded, err := json.Marshal(board["contact_alerts"])
						if err != nil {
							t.Fatal(err)
						}
						var alerts []map[string]any
						if err := json.Unmarshal(encoded, &alerts); err != nil || len(alerts) != 1 ||
							alerts[0]["serial"] != float64(contact) || alerts[0]["resolved_at"] != nil {
							t.Fatalf("presenting coordinator news changed the person's unresolved contact: %s (%v)", encoded, err)
						}
						source := call("read_mail", map[string]any{"token": sender, "msg_serial": mail})["message"].(map[string]any)
						if source["state"] != "pending" || source["consumed"] == true {
							t.Fatalf("coordinator presentation consumed source mail: %v", source)
						}
						// A new contact after resolution is fresh news; suppressing every alert
						// or the recipient forever would satisfy quietness alone.
						if _, err := eng.Do(t.Context(), &core.Op{Kind: core.OpContactResolved, ContactSerial: contact}); err != nil {
							t.Fatal("setup resolution:", err)
						}
						next := call("send", map[string]any{"token": sender, "to": "unreachable", "type": "request", "body": "new contact source"})
						changed, err := eng.Do(t.Context(), &core.Op{
							Kind:      core.OpContactEscalate,
							MsgSerial: uint64(next["msg_serial"].(float64)),
						})
						if err != nil || changed["coalesced"] != false || changed["contact_serial"].(uint64) <= contact {
							t.Fatalf("setup: resolution did not allow fresh contact state: %v %v", changed, err)
						}
						newMarker := fmt.Sprintf("contact alert %d ", changed["contact_serial"])
						fresh := call("hook_poll", map[string]any{"session_id": "contact-once-lead", "event": "Stop", "strict_output": true})
						if fresh["decision"] != "block" || !strings.Contains(fmt.Sprint(fresh), newMarker) || strings.Contains(fmt.Sprint(fresh), marker) {
							t.Fatalf("new contact was lost or old contact returned: %v", fresh)
						}
					})
				}
			}
		}
	}
}
