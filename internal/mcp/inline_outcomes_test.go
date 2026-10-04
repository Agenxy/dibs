package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// Enter through the production MCP tools. A response delivered inline is
// already read: neither another pull nor an encrypted restart may repeat it.
func TestInlineSentOutcomesThroughMCPAndRestart(t *testing.T) {
	for _, read := range []string{"check_in", "inbox"} {
		for _, verdict := range []string{"approve", "queue", "deny", "decline", "answer"} {
			t.Run(read+"/"+verdict, func(t *testing.T) {
				dir := t.TempDir()
				srv, _, stop := restartableQueueServer(t, dir)
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					r := toolCall(t, srv, name, args)
					if r["__is_error"] == true {
						t.Fatalf("setup %s: %v", name, r)
					}
					return r
				}
				lead := call("register", map[string]any{"name": "lead", "nonce": "inline-outcome-lead"})["token"].(string)
				worker := call("register", map[string]any{"name": "worker", "nonce": "inline-outcome-worker"})["token"].(string)
				call("check_in", map[string]any{"token": lead})
				call("check_in", map[string]any{"token": worker})
				kind := "request"
				if verdict == "answer" {
					kind = "question"
				}
				n := call("send", map[string]any{"token": lead, "to": "worker", "type": kind, "body": "request-body-must-not-replace-response"})["msg_serial"]
				body := "actual-responder-words-" + verdict
				call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": verdict, "body": body})
				first := fmt.Sprint(call(read, map[string]any{"token": lead})["agent_updates"])
				if !strings.Contains(first, body) || strings.Contains(first, "request-body-must-not-replace-response") {
					t.Errorf("inline response = %s, want responder's words", first)
				}
				if again := fmt.Sprint(call(read, map[string]any{"token": lead})["agent_updates"]); strings.Contains(again, "msg ") {
					t.Errorf("already delivered outcome repeated: %s", again)
				}
				stop()
				srv, _, stop = restartableQueueServer(t, dir)
				defer stop()
				if again := fmt.Sprint(call(read, map[string]any{"token": lead})["agent_updates"]); strings.Contains(again, "msg ") {
					t.Errorf("already delivered outcome repeated after replay: %s", again)
				}
				m := call("read_mail", map[string]any{"token": lead, "msg_serial": n})["message"].(map[string]any)
				if m["outcome_read_serial"] == nil {
					t.Error("inline delivery did not ledger outcome_read")
				}
			})
		}
	}
}

func TestInlineProgressAndDoneUseTheirOwnWords(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r := toolCall(t, srv, name, args)
		if r["__is_error"] == true {
			t.Fatalf("setup %s: %v", name, r)
		}
		return r
	}
	lead := call("register", map[string]any{"name": "lead", "nonce": "inline-progress-lead"})["token"].(string)
	worker := call("register", map[string]any{"name": "worker", "nonce": "inline-progress-worker"})["token"].(string)
	call("check_in", map[string]any{"token": lead})
	call("check_in", map[string]any{"token": worker})
	n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work", "milestones": []string{"real proof"}})["msg_serial"]
	call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve", "body": "approved in my own words"})
	call("check_in", map[string]any{"token": lead})
	for _, disposition := range []string{"progress", "done"} {
		body := "actual-" + disposition + "-words"
		args := map[string]any{"token": worker, "msg_serial": n, "disposition": disposition, "body": body, "deliverable": "https://example.test/" + disposition}
		if disposition == "progress" {
			args["milestone"] = 1
		}
		call("respond", args)
		updates := fmt.Sprint(call("check_in", map[string]any{"token": lead})["agent_updates"])
		if !strings.Contains(updates, body) || !strings.Contains(updates, "https://example.test/"+disposition) {
			t.Errorf("%s update lacks its words/deliverable: %s", disposition, updates)
		}
		if again := fmt.Sprint(call("check_in", map[string]any{"token": lead})["agent_updates"]); strings.Contains(again, "msg ") {
			t.Errorf("%s already-read update repeated: %s", disposition, again)
		}
	}
}
