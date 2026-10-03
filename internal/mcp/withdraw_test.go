package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSenderWithdrawalThroughMCPAndEncryptedRestart(t *testing.T) {
	for _, disposition := range []string{"pending", "queue", "approve"} {
		t.Run(disposition, func(t *testing.T) {
			dir := t.TempDir()
			srv, _, stop := restartableQueueServer(t, dir)
			lead := toolCall(t, srv, "register", map[string]any{"name": "lead", "nonce": "withdraw-lead"})["token"].(string)
			worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "nonce": "withdraw-worker"})["token"].(string)
			out := rpc(t, srv, "2026-07-28", "tools/call", withTasks(map[string]any{"token": lead, "to": "worker", "type": "request", "body": "private", "track": true}))["result"].(map[string]any)
			n := out["_meta"].(map[string]any)["com.dibs/msg_serial"].(float64)
			id := out["taskId"].(string)
			if disposition != "pending" {
				r := toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": disposition})
				want := "approved"
				if disposition == "queue" {
					want = "queued"
				}
				if r["state"] != want {
					t.Fatalf("setup: %v", r)
				}
			}
			r := toolCall(t, srv, "respond", map[string]any{"token": lead, "msg_serial": n, "disposition": "withdraw", "body": "reassigned"})
			if r["state"] != "withdrawn" {
				t.Fatalf("sender withdrawal: %v", r)
			}
			stop()
			srv, _, stop = restartableQueueServer(t, dir)
			checkpoint := toolCall(t, srv, "check_in", map[string]any{"token": worker})
			updates, _ := json.Marshal(checkpoint["agent_updates"])
			if !strings.Contains(string(updates), "withdrew request") || strings.Contains(string(updates), "is delivered") {
				t.Fatalf("restart notice: %s", updates)
			}
			if q, ok := checkpoint["task_queue"].([]any); ok && len(q) != 0 {
				t.Fatalf("queue restored: %v", q)
			}
			if owes, ok := checkpoint["owes"].([]any); ok && len(owes) != 0 {
				t.Fatalf("debt restored: %v", owes)
			}
			mail := toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
			m := mail["message"].(map[string]any)
			if m["state"] != "withdrawn" || m["withdrawal_reason"] != "reassigned" || m["consumed"] != false {
				t.Fatalf("receipt: %v", mail)
			}
			tracked := rpc(t, srv, "2026-07-28", "tasks/get", taskCapabilities(map[string]any{"taskId": id}))["result"].(map[string]any)
			if tracked["status"] != "cancelled" {
				t.Fatalf("task: %v", tracked)
			}
			late := toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "done"})
			if late["__is_error"] != true {
				t.Fatalf("late done: %v", late)
			}
			ack := toolCall(t, srv, "ack", map[string]any{"token": worker, "msg_serial": n})
			if ack["ok"] != true {
				t.Fatalf("ack: %v", ack)
			}
			stop()
			srv, _, _ = restartableQueueServer(t, dir)
			mail = toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
			if mail["message"].(map[string]any)["consumed"] != true {
				t.Fatalf("ack lost: %v", mail)
			}
			checkpoint = toolCall(t, srv, "check_in", map[string]any{"token": worker})
			updates, _ = json.Marshal(checkpoint["agent_updates"])
			if strings.Contains(string(updates), "withdrew request") {
				t.Fatalf("acknowledged receipt repeated: %s", updates)
			}
		})
	}
}

func TestWithdrawalSurfaceOwnershipAndReplacement(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker"})["token"].(string)
	n := toolCall(t, srv, "send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work"})["msg_serial"]
	replacement := toolCall(t, srv, "send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "replacement"})["msg_serial"]
	for _, args := range []map[string]any{
		{"token": worker, "msg_serial": n, "disposition": "withdraw"},
		{"token": lead, "msg_serial": n, "disposition": "withdraw", "milestone": 1},
		{"token": lead, "msg_serial": n, "disposition": "withdraw", "superseded_by": n},
	} {
		r := toolCall(t, srv, "respond", args)
		if r["__is_error"] != true {
			t.Fatalf("malformed or foreign withdrawal accepted: %v", r)
		}
	}
	bad := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{"name": "respond", "arguments": map[string]any{"token": lead, "msg_serial": n, "disposition": "withdraw", "superseded_by": 0.5}})
	if bad["error"] == nil {
		t.Fatalf("fractional replacement accepted: %v", bad)
	}
	r := toolCall(t, srv, "respond", map[string]any{"token": lead, "msg_serial": n, "disposition": "withdraw", "superseded_by": replacement})
	if r["state"] != "withdrawn" || r["superseded_by"] != replacement {
		t.Fatalf("replacement: %v", r)
	}
	mail := toolCall(t, srv, "read_mail", map[string]any{"token": lead, "msg_serial": replacement})
	if mail["message"].(map[string]any)["state"] != "pending" {
		t.Fatalf("withdrawal started replacement: %v", mail)
	}
	legacy := toolCallOn(t, srv, "2025-11-25", "respond", map[string]any{"token": lead, "msg_serial": replacement, "disposition": "withdraw"})
	if legacy["state"] != "withdrawn" {
		t.Fatalf("legacy withdrawal: %v", legacy)
	}
}

func TestWithdrawalEndsTheActualTaskSubscription(t *testing.T) {
	srv, _, _ := restartableQueueServer(t, t.TempDir())
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	toolCall(t, srv, "register", map[string]any{"name": "worker"})
	sent := rpc(t, srv, "2026-07-28", "tools/call", withTasks(map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work", "track": true}))["result"].(map[string]any)
	n := sent["_meta"].(map[string]any)["com.dibs/msg_serial"]
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "tasks", "method": "subscriptions/listen", "params": taskCapabilities(map[string]any{"notifications": map[string]any{"taskIds": []string{sent["taskId"].(string)}}})})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := scanLines(ctx, resp)
	next := func() string {
		t.Helper()
		for {
			select {
			case line, open := <-lines:
				if !open {
					return ""
				}
				if data, ok := strings.CutPrefix(line, "data: "); ok {
					var msg struct {
						Method string `json:"method"`
						Params struct {
							Status string `json:"status"`
						} `json:"params"`
					}
					if err := json.Unmarshal([]byte(data), &msg); err != nil {
						t.Fatal(err)
					}
					if msg.Method == "notifications/tasks" {
						return msg.Params.Status
					}
				}
			case <-ctx.Done():
				t.Fatal("task subscription did not terminate")
			}
		}
	}
	if status := next(); status != "working" {
		t.Fatalf("setup: %s", status)
	}
	r := toolCall(t, srv, "respond", map[string]any{"token": lead, "msg_serial": n, "disposition": "withdraw"})
	if r["state"] != "withdrawn" {
		t.Fatalf("withdraw: %v", r)
	}
	if status := next(); status != "cancelled" {
		t.Fatalf("terminal snapshot: %s", status)
	}
	if status := next(); status != "" {
		t.Fatalf("stream continued: %s", status)
	}
}
