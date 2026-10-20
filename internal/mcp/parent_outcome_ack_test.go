// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParentAckReadsSentUpdatesWithoutBodies(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, update := range []string{"progress", "done", "queue"} {
			t.Run(version+"/"+update, func(t *testing.T) {
				dir := t.TempDir()
				srv, eng, stop := aliasReplayServer(t, dir)
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					return waitingCall(t, srv, version, name, args)
				}
				lead := call("register", map[string]any{"name": "lead", "nonce": "parent-lead"})["token"]
				worker := call("register", map[string]any{"name": "worker", "nonce": "parent-worker"})["token"]
				stranger := call("register", map[string]any{"name": "stranger", "nonce": "parent-stranger"})["token"]
				call("check_in", map[string]any{"token": lead})
				call("check_in", map[string]any{"token": worker})
				var older any
				if update == "queue" {
					older = call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "older queued task"})["msg_serial"]
					call("respond", map[string]any{"token": worker, "msg_serial": older, "disposition": "queue"})
				}
				n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": strings.Repeat("PRIVATE-ORIGINAL ", 200)})["msg_serial"].(float64)
				initial := "approve"
				if update == "queue" {
					initial = "queue"
				}
				call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": initial})
				call("read_mail", map[string]any{"token": lead, "msg_serial": n})
				if update == "queue" {
					call("queue_update", map[string]any{"token": worker, "msg_serial": n, "before": older})
				} else {
					for range 2 {
						call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "body": strings.Repeat("PRIVATE-REPORT ", 100)})
					}
					if update == "done" {
						call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "done", "body": "PRIVATE-COMPLETION"})
					}
				}
				before := mailboxReceipt(t, eng, uint64(n))
				if !before.HasUnreadOutcome() || (update == "queue" && before.QueueChangedSerial <= before.OutcomeReadAt) {
					t.Fatal("setup: no unread update at the parent")
				}
				denied := toolCallOn(t, srv, version, "ack", map[string]any{"token": stranger, "msg_serial": n})
				if denied["code"] != "E_NO_MESSAGE" || !reflect.DeepEqual(before, mailboxReceipt(t, eng, uint64(n))) {
					t.Fatal("stranger changed another conversation")
				}
				got := toolCallOn(t, srv, version, "ack", map[string]any{"token": lead, "msg_serial": n})
				if got["code"] != nil || got["ok"] != true {
					t.Fatalf("parent ack refused own unread updates: %v", got)
				}
				if strings.Contains(fmt.Sprint(got), "PRIVATE-") || got["message"] != nil || got["updates"] != nil {
					t.Fatal("ack returned a message body instead of a receipt")
				}
				after := mailboxReceipt(t, eng, uint64(n))
				if after.OutcomeReadAt < before.LatestOutcomeSerial() {
					t.Fatal("ack failed to read the entire sender prefix")
				}
				unchanged := after
				unchanged.OutcomeReadAt = before.OutcomeReadAt
				if !reflect.DeepEqual(before, unchanged) {
					t.Fatal("sender ack changed the envelope, recipient prefix or work state")
				}
				first, err := os.Stat(filepath.Join(dir, "ledger.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				call("ack", map[string]any{"token": lead, "msg_serial": n})
				second, err := os.Stat(filepath.Join(dir, "ledger.jsonl"))
				if err != nil || first.Size() != second.Size() || !reflect.DeepEqual(after, mailboxReceipt(t, eng, uint64(n))) {
					t.Fatal("repeated parent ack changed replayable state")
				}
				stop()
				_, eng, stop = aliasReplayServer(t, dir)
				defer stop()
				if restored := mailboxReceipt(t, eng, uint64(n)); !reflect.DeepEqual(after, restored) {
					t.Fatal("sender ack was not durable through encrypted replay")
				}
			})
		}
	}
}

func TestParentAckReadsRecipientReviewsOnly(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			srv, eng, stop := aliasReplayServer(t, dir)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return waitingCall(t, srv, version, name, args)
			}
			lead := call("register", map[string]any{"name": "lead", "nonce": "review-parent-lead"})["token"]
			worker := call("register", map[string]any{"name": "worker", "nonce": "review-parent-worker"})["token"]
			call("check_in", map[string]any{"token": lead})
			call("check_in", map[string]any{"token": worker})
			n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "PRIVATE-TASK", "milestones": []string{"proof"}})["msg_serial"].(float64)
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve"})
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": "first proof"})
			for range 2 {
				call("respond", map[string]any{"token": lead, "msg_serial": n, "disposition": "flag", "milestone": 1, "body": strings.Repeat("PRIVATE-REVIEW ", 150)})
			}
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": strings.Repeat("PRIVATE-NEW-PROOF ", 150)})
			before := mailboxReceipt(t, eng, uint64(n))
			if before.LatestReviewSerial() <= before.ReviewReadAt || !before.HasUnreadOutcome() {
				t.Fatal("setup: both parties must have independent unread units")
			}
			got := call("ack", map[string]any{"token": worker, "msg_serial": n})
			if strings.Contains(fmt.Sprint(got), "PRIVATE-") {
				t.Fatal("review ack returned private text")
			}
			after := mailboxReceipt(t, eng, uint64(n))
			if after.ReviewReadAt < before.LatestReviewSerial() {
				t.Fatal("parent ack left recipient reviews unread")
			}
			unchanged := after
			unchanged.ReviewReadAt = before.ReviewReadAt
			if !reflect.DeepEqual(before, unchanged) {
				t.Fatal("recipient review ack changed the sender prefix or request")
			}
			call("ack", map[string]any{"token": worker, "msg_serial": n})
			if !reflect.DeepEqual(after, mailboxReceipt(t, eng, uint64(n))) {
				t.Fatal("repeated review ack changed the request")
			}
			stop()
			_, eng, stop = aliasReplayServer(t, dir)
			defer stop()
			if restored := mailboxReceipt(t, eng, uint64(n)); !reflect.DeepEqual(after, restored) {
				t.Fatal("review ack was not durable through encrypted replay")
			}
		})
	}
}

func TestParentAckKeepsPendingAndSelfMailSemantics(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, _ := aliasReplayServer(t, t.TempDir())
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				return waitingCall(t, srv, version, name, args)
			}
			lead := call("register", map[string]any{"name": "lead", "nonce": "self-parent-lead"})["token"]
			worker := call("register", map[string]any{"name": "worker", "nonce": "self-parent-worker"})["token"]
			call("check_in", map[string]any{"token": lead})
			n := call("send", map[string]any{"token": lead, "to": "worker", "type": "question", "body": "pending ask"})["msg_serial"].(float64)
			before := mailboxReceipt(t, eng, uint64(n))
			refused := toolCallOn(t, srv, version, "ack", map[string]any{"token": lead, "msg_serial": n})
			if refused["code"] != "E_NOT_TERMINAL" || !reflect.DeepEqual(before, mailboxReceipt(t, eng, uint64(n))) {
				t.Fatal("pending sent ask claimed an outcome or changed state")
			}
			call("ack", map[string]any{"token": worker, "msg_serial": n})
			if mailboxReceipt(t, eng, uint64(n)).State != "acked" {
				t.Fatal("ordinary incoming acknowledgement changed semantics")
			}
			self := call("send", map[string]any{"token": lead, "to": "lead", "type": "question", "body": "self ask"})["msg_serial"].(float64)
			call("ack", map[string]any{"token": lead, "msg_serial": self})
			if mailboxReceipt(t, eng, uint64(self)).State != "acked" {
				t.Fatal("pending self-addressed ask cannot be acknowledged")
			}
			call("respond", map[string]any{"token": lead, "msg_serial": self, "disposition": "answer", "body": "PRIVATE-SELF-ANSWER"})
			before = mailboxReceipt(t, eng, uint64(self))
			if !before.HasUnreadOutcome() {
				t.Fatal("setup: self outcome was already read")
			}
			call("ack", map[string]any{"token": lead, "msg_serial": self})
			after := mailboxReceipt(t, eng, uint64(self))
			if after.OutcomeReadAt < before.LatestOutcomeSerial() || after.State != before.State || after.Consumed != before.Consumed {
				t.Fatal("self-addressed outcome acknowledgement did not preserve the envelope")
			}
		})
	}
}
