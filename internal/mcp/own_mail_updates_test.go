// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// Both protocol paths enter the authenticated production tools and rebuild
// from the encrypted ledger. No read watermark or notice is set by the test.
func TestOwnReadMailShowsOnlyUnreadUpdates(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, verdict := range []string{"progress", "done", "answer"} {
			t.Run(version+"/"+verdict, func(t *testing.T) {
				dir := t.TempDir()
				srv, eng, stop := aliasReplayServer(t, dir)
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					return waitingCall(t, srv, version, name, args)
				}
				lead := call("register", map[string]any{"name": "lead", "nonce": "own-mail-lead"})["token"].(string)
				worker := call("register", map[string]any{"name": "worker", "nonce": "own-mail-worker"})["token"].(string)
				call("check_in", map[string]any{"token": lead})
				call("check_in", map[string]any{"token": worker})
				body := strings.Repeat("original request content ", 180) + "ORIGINAL-BODY-TAIL"
				kind := "request"
				if verdict == "answer" {
					kind = "question"
				}
				send := map[string]any{"token": lead, "to": "worker", "type": kind, "body": body}
				if kind == "request" {
					send["milestones"] = []string{"first original milestone", "second original milestone"}
				}
				n := call("send", send)["msg_serial"].(float64)
				read := map[string]any{"token": lead, "msg_serial": n}
				if got := call("read_mail", read)["message"].(map[string]any)["body"]; got != body {
					t.Fatal("setup: own pending message lost its full envelope")
				}
				if got := call("read_mail", map[string]any{"token": worker, "msg_serial": n})["message"].(map[string]any)["body"]; got != body {
					t.Fatal("recipient did not receive the full body")
				}
				if kind == "request" {
					call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve", "body": "old approval"})
					call("read_mail", read)
					call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "body": "old progress"})
					call("read_mail", read)
				}
				fresh := "fresh report\n```python\nif ready:\n    run()\n```"
				reportArgs := map[string]any{"token": worker, "msg_serial": n, "disposition": verdict, "body": fresh}
				if verdict != "answer" {
					reportArgs["deliverable"] = "https://example.test/proof"
				}
				report := call("respond", reportArgs)
				if got := call("read_mail", map[string]any{"token": worker, "msg_serial": n})["message"].(map[string]any)["body"]; got != body {
					t.Fatal("recipient lost the original task while the sender had unread updates")
				}
				before := mailboxReceipt(t, eng, uint64(n))
				if !before.HasUnreadOutcome() {
					t.Fatalf("setup: report has no unread outcome: %v", report)
				}
				got := call("read_mail", read)
				message := got["message"].(map[string]any)
				if _, repeated := message["body"]; repeated || strings.Contains(fmt.Sprint(got), "ORIGINAL-BODY-TAIL") {
					t.Fatal("own outcome read repeated the original request body")
				}
				if message["body_omitted"] != true || !strings.Contains(fmt.Sprint(message["read_hint"]), "include_body") {
					t.Fatalf("compact reply has no full-envelope recovery: %v", got)
				}
				units, ok := got["updates"].([]any)
				wantBody := fresh
				if verdict == "done" {
					wantBody = before.Response
				}
				if !ok || len(units) != 1 || units[0].(map[string]any)["body"] != wantBody {
					t.Fatalf("fresh update or exact formatting lost: %v", got)
				}
				if verdict == "done" && units[0].(map[string]any)["label"] != "response (includes any earlier approval text)" {
					t.Fatal("stored completion response did not explain its approval prefix")
				}
				if panel := fmt.Sprint(panelMeta(got)); strings.Contains(panel, "fresh report") || strings.Contains(panel, "old approval") {
					t.Fatal("a private update body escaped into the panel copy")
				}
				if strings.Contains(fmt.Sprint(got), "old progress") || (verdict != "done" && strings.Contains(fmt.Sprint(got), "old approval")) || got["milestone_reviews"] != nil {
					t.Fatal("already-read history or original milestones repeated")
				}
				after := mailboxReceipt(t, eng, uint64(n))
				if after.OutcomeReadAt < before.LatestOutcomeSerial() || after.Body != body || after.Consumed != before.Consumed {
					t.Fatalf("durable read or retained envelope changed: %+v", after)
				}
				full := call("read_mail", map[string]any{"token": lead, "msg_serial": n, "include_body": true})
				if full["message"].(map[string]any)["body"] != body {
					t.Fatal("explicit full-envelope read lost original content")
				}
				stop()
				srv, eng, stop = aliasReplayServer(t, dir)
				defer stop()
				if restored := mailboxReceipt(t, eng, uint64(n)); restored.OutcomeReadAt != after.OutcomeReadAt || restored.Body != body {
					t.Fatal("compact read did not survive encrypted replay")
				}
				if updates := fmt.Sprint(call("check_in", map[string]any{"token": lead})["agent_updates"]); strings.Contains(updates, "fresh report") {
					t.Fatal("already-read update repeated after replay")
				}
			})
		}
	}
}

func TestReadMailIncludeBodyOverridesUnreadUpdates(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, _ := aliasReplayServer(t, t.TempDir())
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return waitingCall(t, srv, version, name, args)
			}
			lead := call("register", map[string]any{"name": "lead", "nonce": "full-read-lead"})["token"].(string)
			worker := call("register", map[string]any{"name": "worker", "nonce": "full-read-worker"})["token"].(string)
			call("check_in", map[string]any{"token": lead})
			call("check_in", map[string]any{"token": worker})
			n := call("send", map[string]any{
				"token": lead, "to": "worker", "type": "question", "body": "complete original body",
				"attachments": []any{map[string]any{"path": "/fixture/original-proof"}},
			})["msg_serial"].(float64)
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "answer", "body": "complete response"})
			before := mailboxReceipt(t, eng, uint64(n))
			if !before.HasUnreadOutcome() {
				t.Fatal("setup: response was already read")
			}
			got := call("read_mail", map[string]any{"token": lead, "msg_serial": n, "include_body": true})
			m := got["message"].(map[string]any)
			if m["body"] != "complete original body" || m["response"] != "complete response" || got["updates"] != nil {
				t.Fatalf("include_body did not force the full envelope: %v", got)
			}
			attachments, ok := m["attachments"].([]any)
			if !ok || len(attachments) != 1 || attachments[0].(map[string]any)["path"] != "/fixture/original-proof" {
				t.Fatal("full read lost the original attachment reference")
			}
			if mailboxReceipt(t, eng, uint64(n)).OutcomeReadAt < before.LatestOutcomeSerial() {
				t.Fatal("explicit full read lost its durable read record")
			}
			if seen := call("read_mail", map[string]any{"token": lead, "msg_serial": n}); seen["message"].(map[string]any)["body"] != "complete original body" || seen["updates"] != nil {
				t.Fatal("a later read with no unread updates was not full")
			}
			self := call("send", map[string]any{"token": lead, "to": "lead", "type": "question", "body": "my own reminder"})["msg_serial"]
			call("respond", map[string]any{"token": lead, "msg_serial": self, "disposition": "answer", "body": "my own answer"})
			if selfRead := call("read_mail", map[string]any{"token": lead, "msg_serial": self}); selfRead["message"].(map[string]any)["body"] != "my own reminder" || selfRead["updates"] != nil {
				t.Fatal("self-addressed mail did not keep its full envelope")
			}
		})
	}
}
