// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

type adoptionFixture struct {
	t                           *testing.T
	srv                         *httptest.Server
	eng                         *engine.Engine
	stop                        func()
	version, lead, heir, sender string
}

func newAdoptionFixture(t *testing.T, version, dir string) *adoptionFixture {
	t.Helper()
	srv, eng, stop := restartableQueueServer(t, dir)
	f := &adoptionFixture{t: t, srv: srv, eng: eng, stop: stop, version: version}
	register := func(name string) string {
		r := f.call("register", map[string]any{"name": name, "nonce": "adoption-" + name, "session_id": "adoption-session-" + name})
		token, ok := r["token"].(string)
		if !ok || token == "" {
			t.Fatalf("setup register: %v", r)
		}
		f.call("check_in", map[string]any{"token": token})
		return token
	}
	f.lead, f.heir, f.sender = register("lead"), register("heir"), register("sender")
	register("lost")
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator}); err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": f.lead})
	return f
}

func (f *adoptionFixture) call(name string, args map[string]any) map[string]any {
	f.t.Helper()
	r := toolCallOn(f.t, f.srv, f.version, name, args)
	if r["error"] != nil || r["code"] != nil || r["__is_error"] == true {
		f.t.Fatalf("%s: %v", name, r)
	}
	return r
}

func (f *adoptionFixture) abandon() {
	f.t.Helper()
	if _, err := f.eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"lost"}}); err != nil {
		f.t.Fatal("setup abandonment:", err)
	}
}

func TestAdoptionRequestClosesAfterDirectAdoptionThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			f := newAdoptionFixture(t, version, dir)
			f.call("send", map[string]any{"token": f.sender, "to": "lost", "type": "request", "body": "recover this work"})
			request := f.call("send", map[string]any{"token": f.heir, "to": "lead", "type": "request", "adopt": "lost", "body": "recover my abandoned mailbox"})["msg_serial"]
			f.abandon()
			adopted := f.call("adopt_agent", map[string]any{"token": f.lead, "agent": "lost", "into": "heir"})
			if adopted["messages"] != float64(1) {
				t.Fatalf("setup did not move exactly one message: %v", adopted)
			}
			f.stop()
			f.srv, f.eng, f.stop = restartableQueueServer(t, dir)
			read := f.call("read_mail", map[string]any{"token": f.lead, "msg_serial": request})
			if read["message"].(map[string]any)["prior_adoption_serial"] != adopted["serial"] {
				t.Fatalf("replay lost the request's completed adoption: %v", read)
			}
			approved := f.call("respond", map[string]any{"token": f.lead, "msg_serial": request, "disposition": "approve"})
			if approved["state"] != "approved" || approved["messages"] != float64(0) || approved["prior_adoption_serial"] != adopted["serial"] {
				t.Fatalf("approval did not record the earlier completed adoption: %v; adoption %v", approved, adopted)
			}
			f.stop()
			f.srv, f.eng, f.stop = restartableQueueServer(t, dir)
			done := f.call("respond", map[string]any{"token": f.lead, "msg_serial": request, "disposition": "done"})
			if done["state"] != "done" {
				t.Fatalf("completed adoption request remains open: %v", done)
			}
			f.stop()
			f.srv, f.eng, f.stop = restartableQueueServer(t, dir)
			read = f.call("read_mail", map[string]any{"token": f.heir, "msg_serial": request})
			if read["message"].(map[string]any)["state"] != "done" {
				t.Fatalf("replay lost completed adoption request: %v", read)
			}
		})
	}
}

func TestAdoptionResolvesSourceContactThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, route := range []string{"direct", "approval"} {
			t.Run(version+"/"+route, func(t *testing.T) {
				f := newAdoptionFixture(t, version, t.TempDir())
				sent := f.call("send", map[string]any{"token": f.sender, "to": "lost", "type": "request", "body": "unread work"})["msg_serial"].(float64)
				contact, err := f.eng.Do(context.Background(), &core.Op{Kind: core.OpContactEscalate, MsgSerial: uint64(sent)})
				if err != nil || contact["coalesced"] != false {
					t.Fatalf("setup contact: %v %v", contact, err)
				}
				f.abandon()
				if route == "direct" {
					f.call("adopt_agent", map[string]any{"token": f.lead, "agent": "lost", "into": "heir"})
				} else {
					request := f.call("send", map[string]any{"token": f.heir, "to": "lead", "type": "request", "adopt": "lost", "body": "recover my mailbox"})["msg_serial"]
					f.call("respond", map[string]any{"token": f.lead, "msg_serial": request, "disposition": "approve"})
					read := f.call("read_mail", map[string]any{"token": f.lead, "msg_serial": request})
					if read["message"].(map[string]any)["prior_adoption_serial"] != nil {
						t.Fatalf("ordinary approval fabricated an earlier adoption receipt: %v", read)
					}
					done := toolCallOn(t, f.srv, version, "respond", map[string]any{"token": f.lead, "msg_serial": request, "disposition": "done"})
					if done["code"] != "E_BAD_DISPOSITION" {
						t.Fatalf("ordinary approved adoption unexpectedly allowed done: %v", done)
					}
				}
				board, err := f.eng.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if board["contact_alerts"] != nil {
					t.Fatalf("adoption left a source contact alert: %v", board["contact_alerts"])
				}
				// The inherited unread message must be eligible for the destination's
				// ordinary contact path, rather than deduplicated against the source.
				next, err := f.eng.Do(context.Background(), &core.Op{Kind: core.OpContactEscalate, MsgSerial: uint64(sent)})
				if err != nil || next["deduplicated"] == true || next["contact_serial"] == contact["contact_serial"] {
					t.Fatalf("destination contact blocked by old receipt: %v %v", next, err)
				}
				board, err = f.eng.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				b, err := json.Marshal(board["contact_alerts"])
				if err != nil {
					t.Fatal(err)
				}
				var alerts []map[string]any
				if err := json.Unmarshal(b, &alerts); err != nil || len(alerts) != 1 || alerts[0]["recipient"] != "heir" {
					t.Fatalf("wrong contact owner: %s (%v)", b, err)
				}
			})
		}
	}
}

func TestPriorAdoptionDoesNotHideUnperformedWorkThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, situation := range []string{"never-adopted", "other-destination", "before-request", "new-arrival", "ordinary-responder"} {
			t.Run(version+"/"+situation, func(t *testing.T) {
				f := newAdoptionFixture(t, version, t.TempDir())
				if situation == "other-destination" {
					other := f.call("register", map[string]any{"name": "other", "nonce": "other-adoption", "session_id": "other-adoption-session"})["token"]
					f.call("check_in", map[string]any{"token": other})
				}
				to, responder := "lead", f.lead
				if situation == "ordinary-responder" {
					to, responder = "sender", f.sender
				}
				sendRequest := func() any {
					return f.call("send", map[string]any{"token": f.heir, "to": to, "type": "request", "adopt": "lost", "body": "recover"})["msg_serial"]
				}
				var request any
				if situation != "before-request" {
					request = sendRequest()
				}
				if situation != "never-adopted" {
					f.call("send", map[string]any{"token": f.sender, "to": "lost", "type": "request", "body": "existing work"})
				}
				f.abandon()
				if situation != "never-adopted" {
					into := "heir"
					if situation == "other-destination" {
						into = "other"
					}
					f.call("adopt_agent", map[string]any{"token": f.lead, "agent": "lost", "into": into})
				}
				if situation == "before-request" {
					request = sendRequest()
				}
				if situation == "new-arrival" {
					f.call("send", map[string]any{"token": f.sender, "to": "lost", "type": "request", "body": "later work"})
				}
				out := toolCallOn(t, f.srv, version, "respond", map[string]any{"token": responder, "msg_serial": request, "disposition": "approve"})
				if situation == "new-arrival" {
					if out["state"] != "approved" || out["messages"] != float64(1) || out["prior_adoption_serial"] != nil {
						t.Fatalf("new mail was hidden by earlier adoption: %v", out)
					}
					read := f.call("read_mail", map[string]any{"token": responder, "msg_serial": request})
					if read["message"].(map[string]any)["prior_adoption_serial"] != nil {
						t.Fatalf("fresh approval retained stale receipt: %v", read)
					}
				} else {
					want := "E_NOTHING_TO_ADOPT"
					if situation == "ordinary-responder" {
						want = "E_NOT_PERMITTED"
					}
					if out["code"] != want {
						t.Fatalf("%s approved without the required effect/authority: %v", situation, out)
					}
				}
			})
		}
	}
}
