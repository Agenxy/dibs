package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func TestMilestoneEventAcknowledgmentAndReviewThroughMCP(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r := toolCall(t, srv, name, args)
		if r["__is_error"] == true {
			t.Fatalf("setup %s: %v", name, r)
		}
		return r
	}
	tokens := map[string]string{}
	for _, id := range []string{"lead", "worker", "stranger"} {
		tokens[id] = call("register", map[string]any{"name": id, "nonce": "milestone-ux-" + id})["token"].(string)
		call("check_in", map[string]any{"token": tokens[id]})
	}
	parent := call("send", map[string]any{"token": tokens["lead"], "to": "worker", "type": "request", "body": "build proof", "milestones": []string{"proof"}})["msg_serial"]
	call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "approve"})
	initial := call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
	initialReview := initial["milestone_reviews"].([]any)[0].(map[string]any)
	if initialReview["status"] != "unreviewed" || initialReview["at"] != nil {
		t.Fatalf("unreported milestone has an invented timestamp: %v", initialReview)
	}
	call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "progress", "milestone": 1, "body": "proof ready"})
	events := call("events_since", map[string]any{"token": tokens["lead"], "since_serial": parent})["events"].([]any)
	var event any
	for _, raw := range events {
		e := raw.(map[string]any)
		if e["type"] == "message.progress" {
			event = e["serial"]
		}
	}
	if event == nil {
		t.Fatal("setup: progress event missing")
	}
	call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "progress", "milestone": 1, "body": "another report"})
	updates := call("inbox", map[string]any{"token": tokens["lead"]})["agent_updates"].([]any)
	if len(updates) != 2 {
		t.Fatalf("setup: expected two progress notices: %v", updates)
	}
	denied := toolCall(t, srv, "ack", map[string]any{"token": tokens["stranger"], "msg_serial": event})
	if denied["__is_error"] != true || strings.Contains(fmt.Sprint(denied), fmt.Sprintf("read_mail(msg_serial:%.0f)", parent)) {
		t.Fatalf("stranger acknowledgment or parent leak: %v", denied)
	}
	acked := call("ack", map[string]any{"token": tokens["lead"], "msg_serial": event})
	if acked["state"] != "acked" {
		t.Fatalf("event acknowledgment: %v", acked)
	}
	updates, _ = call("inbox", map[string]any{"token": tokens["lead"]})["agent_updates"].([]any)
	if len(updates) != 0 {
		t.Fatalf("reports already quoted by inbox repeated after event ack: %v", updates)
	}
	wrong := toolCall(t, srv, "respond", map[string]any{"token": tokens["lead"], "msg_serial": event, "disposition": "accept", "milestone": 1})
	if wrong["__is_error"] != true || !strings.Contains(fmt.Sprint(wrong["hint"]), fmt.Sprintf("respond(msg_serial:%.0f", parent)) {
		t.Fatalf("wrong event lacks corrective review call: %v", wrong)
	}
	call("ack", map[string]any{"token": tokens["lead"], "msg_serial": event}) // idempotent after read
	read := call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
	statuses, ok := read["milestone_reviews"].([]any)
	if !ok || len(statuses) != 1 || statuses[0].(map[string]any)["status"] != "unreviewed" {
		t.Fatalf("seen is not review: %v", read)
	}
	call("respond", map[string]any{"token": tokens["lead"], "msg_serial": parent, "disposition": "accept", "milestone": 1})
	read = call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
	if read["milestone_reviews"].([]any)[0].(map[string]any)["status"] != "accepted" {
		t.Fatalf("review missing: %v", read)
	}
	review := read["milestone_reviews"].([]any)[0].(map[string]any)
	if review["by"] != "lead" || review["at"] == nil {
		t.Fatalf("review omitted who/when: %v", review)
	}
	call("send", map[string]any{"token": tokens["lead"], "to": "worker", "type": "notify", "body": "thanks"})
	call("respond", map[string]any{"token": tokens["lead"], "msg_serial": parent, "disposition": "flag", "milestone": 1, "body": "change proof"})
	read = call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
	if read["milestone_reviews"].([]any)[0].(map[string]any)["status"] != "flagged" {
		t.Fatalf("flag missing: %v", read)
	}
	call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "progress", "milestone": 1, "body": "revised proof"})
	read = call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
	if read["milestone_reviews"].([]any)[0].(map[string]any)["status"] != "unreviewed" {
		t.Fatalf("new report inherited old acceptance: %v", read)
	}
}
