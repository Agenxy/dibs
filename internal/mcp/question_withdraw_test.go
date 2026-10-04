package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionWithdrawalThroughMCPAndEncryptedRestart(t *testing.T) {
	for _, state := range []string{"pending", "delivered", "acked"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			srv, _, stop := restartableQueueServer(t, dir)
			lead := toolCall(t, srv, "register", map[string]any{"name": "lead", "nonce": "question-lead"})["token"]
			worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "nonce": "question-worker"})["token"]
			n := toolCall(t, srv, "send", map[string]any{
				"token": lead, "to": "worker", "type": "question", "body": "Still needed?",
			})["msg_serial"]
			if n == nil {
				t.Fatal("setup: question was not sent")
			}
			switch state {
			case "delivered":
				mail := toolCall(t, srv, "inbox", map[string]any{"token": worker})
				if len(mail["messages"].([]any)) != 1 {
					t.Fatalf("setup: %v", mail)
				}
			case "acked":
				r := toolCall(t, srv, "ack", map[string]any{"token": worker, "msg_serial": n})
				if r["state"] != "acked" {
					t.Fatalf("setup: %v", r)
				}
			}
			r := toolCall(t, srv, "respond", map[string]any{
				"token": lead, "msg_serial": n, "disposition": "withdraw", "body": "answered elsewhere",
			})
			if r["state"] != "withdrawn" {
				t.Fatalf("question withdrawal: %v", r)
			}
			stop()
			srv, _, stop = restartableQueueServer(t, dir)
			checkpoint := toolCall(t, srv, "check_in", map[string]any{"token": worker})
			updates, _ := json.Marshal(checkpoint["agent_updates"])
			if !strings.Contains(string(updates), "withdrew question") || strings.Contains(string(updates), "answered") {
				t.Fatalf("restart receipt: %s", updates)
			}
			mail := toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
			m := mail["message"].(map[string]any)
			if m["state"] != "withdrawn" || m["withdrawal_reason"] != "answered elsewhere" || m["consumed"] != false {
				t.Fatalf("durable receipt: %v", mail)
			}
			late := toolCall(t, srv, "respond", map[string]any{
				"token": worker, "msg_serial": n, "disposition": "answer", "body": "late",
			})
			if late["__is_error"] != true {
				t.Fatalf("withdrawn question answered: %v", late)
			}
			ack := toolCall(t, srv, "ack", map[string]any{"token": worker, "msg_serial": n})
			if ack["ok"] != true {
				t.Fatalf("ack: %v", ack)
			}
			stop()
			srv, _, _ = restartableQueueServer(t, dir)
			checkpoint = toolCall(t, srv, "check_in", map[string]any{"token": worker})
			updates, _ = json.Marshal(checkpoint["agent_updates"])
			if strings.Contains(string(updates), "withdrew question") {
				t.Fatalf("acknowledged withdrawal repeated: %s", updates)
			}
		})
	}
}
