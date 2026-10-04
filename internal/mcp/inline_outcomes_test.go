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

func TestProgressAckSurvivesEncryptedRestart(t *testing.T) {
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
	lead := call("register", map[string]any{"name": "lead", "nonce": "progress-ack-restart-lead"})["token"]
	worker := call("register", map[string]any{"name": "worker", "nonce": "progress-ack-restart-worker"})["token"]
	call("check_in", map[string]any{"token": lead})
	n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work"})["msg_serial"]
	call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve"})
	call("read_mail", map[string]any{"token": lead, "msg_serial": n})
	call("respond", map[string]any{
		"token": worker, "msg_serial": n,
		"disposition": "progress", "body": "persist-dismissed-report",
	})
	// Worker reads its own report without reading the independent sender view.
	m := call("read_mail", map[string]any{"token": worker, "msg_serial": n})["message"].(map[string]any)
	event := m["progress"].([]any)[0].(map[string]any)["serial"]
	call("ack", map[string]any{"token": lead, "msg_serial": event})
	stop()
	srv, _, stop = restartableQueueServer(t, dir)
	defer stop()
	if got := fmt.Sprint(call("check_in", map[string]any{"token": lead})["agent_updates"]); strings.Contains(got, "persist-dismissed-report") {
		t.Error("dismissed progress returned after encrypted replay")
	}
}

func TestWithdrawalDoesNotResurrectReportsOrConsumeItsReceipt(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r := toolCall(t, srv, name, args)
		if r["__is_error"] == true {
			t.Fatalf("setup %s: %v", name, r)
		}
		return r
	}
	lead := call("register", map[string]any{"name": "lead", "nonce": "withdraw-inline-lead"})["token"]
	worker := call("register", map[string]any{"name": "worker", "nonce": "withdraw-inline-worker"})["token"]
	call("check_in", map[string]any{"token": lead})
	n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work"})["msg_serial"]
	call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve"})
	call("respond", map[string]any{
		"token": worker, "msg_serial": n,
		"disposition": "progress", "body": "cancelled-old-report",
	})
	call("respond", map[string]any{
		"token": lead, "msg_serial": n,
		"disposition": "withdraw", "body": "actual withdrawal reason",
	})
	if got := fmt.Sprint(call("check_in", map[string]any{"token": lead})["agent_updates"]); strings.Contains(got, "cancelled-old-report") {
		t.Error("withdrawal resurrected an old report")
	}
	if got := fmt.Sprint(call("check_in", map[string]any{"token": worker})["agent_updates"]); !strings.Contains(got, "actual withdrawal reason") {
		t.Error("worker did not receive the actual withdrawal reason")
	}
	m := call("read_mail", map[string]any{"token": worker, "msg_serial": n})["message"].(map[string]any)
	if m["consumed"] == true {
		t.Error("quoted withdrawal reason consumed its explicit receipt")
	}
	call("ack", map[string]any{"token": worker, "msg_serial": n})
}

func TestFlagAfterDoneSurvivesEncryptedRestartAndReadsOnce(t *testing.T) {
	for _, read := range []string{"check_in", "read_mail", "ack"} {
		t.Run(read, func(t *testing.T) {
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
			lead := call("register", map[string]any{"name": "lead", "nonce": "flag-restart-lead"})["token"].(string)
			worker := call("register", map[string]any{"name": "worker", "nonce": "flag-restart-worker"})["token"].(string)
			call("check_in", map[string]any{"token": lead})
			call("check_in", map[string]any{"token": worker})
			n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work", "milestones": []string{"proof"}})["msg_serial"]
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve"})
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": "original proof"})
			call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "done", "body": "delivered"})
			call("read_mail", map[string]any{"token": lead, "msg_serial": n})
			call("respond", map[string]any{"token": lead, "msg_serial": n, "disposition": "flag", "milestone": 1, "body": "flag-after-done-real-words"})
			flagged := call("read_mail", map[string]any{"token": lead, "msg_serial": n}) // must not consume worker's view
			entries := flagged["message"].(map[string]any)["progress"].([]any)
			review := entries[len(entries)-1].(map[string]any)["serial"]
			stop()
			srv, _, stop = restartableQueueServer(t, dir)
			defer stop()
			switch read {
			case "ack":
				call("ack", map[string]any{"token": worker, "msg_serial": review})
				call("ack", map[string]any{"token": worker, "msg_serial": review})
			case "read_mail":
				call("read_mail", map[string]any{"token": worker, "msg_serial": n})
			default:
				first := fmt.Sprint(call(read, map[string]any{"token": worker})["agent_updates"])
				if !strings.Contains(first, "flag-after-done-real-words") {
					t.Errorf("flag vanished on restart or sender consumed worker view: %s", first)
				}
			}
			if again := fmt.Sprint(call("check_in", map[string]any{"token": worker})["agent_updates"]); strings.Contains(again, "FLAGGED") {
				t.Errorf("review repeated after read: %s", again)
			}
			stop()
			srv, _, stop = restartableQueueServer(t, dir)
			defer stop()
			if again := fmt.Sprint(call("check_in", map[string]any{"token": worker})["agent_updates"]); strings.Contains(again, "FLAGGED") {
				t.Errorf("read review repeated after restart: %s", again)
			}
			if again := fmt.Sprint(call("check_in", map[string]any{"token": lead})["agent_updates"]); strings.Contains(again, "msg ") {
				t.Errorf("sender already read outcomes repeated: %s", again)
			}
		})
	}
}
