// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

func waitingCall(t *testing.T, srv *httptest.Server, version, name string, args map[string]any) map[string]any {
	t.Helper()
	out := rpc(t, srv, version, "tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := out["result"].(map[string]any)
	if !ok || result["isError"] == true {
		t.Fatalf("setup %s: %v", name, out)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("setup %s: %v", name, err)
	}
	return payload
}

// Only the fixture's clock is historical. The state comes through the normal
// encrypted ledger append/replay door, and every delivery/read/new-mail action
// below enters through authenticated MCP. No notice cache or read flag is set
// by hand: the production boot rebuilds the old verdict, and inbox consumes it.
func historicalWaitingServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "waiting", box)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	st := core.NewState("waiting", core.DefaultLimits())
	appendOp := func(op *core.Op) core.Result {
		t.Helper()
		result, _, applyErr := st.Apply(op, old)
		if applyErr != nil {
			t.Fatalf("setup %s: %v", op.Kind, applyErr)
		}
		if appendErr := journal.Append(st.Serial, old, op); appendErr != nil {
			t.Fatal(appendErr)
		}
		return result
	}
	for _, name := range []string{"lead", "worker"} {
		appendOp(&core.Op{Kind: core.OpRegister, Name: name, NewToken: name + "-token", AgentKind: core.KindPersistent, Nonce: name + "-nonce"})
		appendOp(&core.Op{Kind: core.OpAckBoard, Token: name + "-token"})
	}
	// Approved work stays retained under the unchanged obligation window, so
	// boot cannot erase the old outcome before the MCP read actually sees it.
	sent := appendOp(&core.Op{Kind: core.OpSendMessage, Token: "lead-token", To: "worker", MsgType: core.MsgRequest, Body: "old request", DeadlineSec: 600})
	appendOp(&core.Op{Kind: core.OpRespond, Token: "worker-token", MsgSerial: sent["msg_serial"].(uint64), Disposition: "approve", Body: "historical-answer-marker"})
	replayed := core.NewState("waiting", core.DefaultLimits())
	if _, err = journal.Replay(replayed); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(replayed, journal, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	})
	return srv
}

func TestWaitingAgeIgnoresAnOutcomeReadThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, fresh := range []string{"mail", "update"} {
			t.Run(version+"/"+fresh, func(t *testing.T) {
				srv := historicalWaitingServer(t)
				call := func(name string, args map[string]any) map[string]any {
					return waitingCall(t, srv, version, name, args)
				}
				read := call("inbox", map[string]any{"token": "lead-token"})
				updates, ok := read["agent_updates"].([]any)
				if !ok || len(updates) != 1 || !strings.Contains(updates[0].(string), "historical-answer-marker") {
					t.Fatalf("setup: old verdict was not actually delivered: %v", read)
				}
				read = call("inbox", map[string]any{"token": "lead-token"})
				if updates, ok := read["agent_updates"].([]any); ok && len(updates) != 0 {
					t.Fatalf("setup: old verdict remained unread: %v", read)
				}
				want := "1 new"
				if fresh == "mail" {
					call("send", map[string]any{"token": "worker-token", "to": "lead", "type": "question", "body": "fresh mail marker"})
				} else {
					sent := call("send", map[string]any{"token": "lead-token", "to": "worker", "type": "question", "body": "fresh question"})
					call("respond", map[string]any{"token": "worker-token", "msg_serial": sent["msg_serial"], "disposition": "answer", "body": "fresh update marker"})
					want = "1 update(s) to you"
				}
				written := call("update", map[string]any{"token": "lead-token", "description": "waiting age probe"})
				waiting, _ := written["waiting"].(string)
				if !strings.Contains(waiting, want) {
					t.Fatalf("setup: fresh item was not counted: %v", written)
				}
				if strings.Contains(waiting, "has been waiting") {
					t.Errorf("fresh %s inherited the age of an already-read outcome: %q", fresh, waiting)
				}
				visible := call("inbox", map[string]any{"token": "lead-token"})
				if fresh == "mail" {
					mail, ok := visible["inbox"].([]any)
					if !ok || len(mail) != 1 || mail[0].(map[string]any)["body"] != "fresh mail marker" {
						t.Fatalf("counted mail is invisible: %v", visible)
					}
				} else {
					updates, ok := visible["agent_updates"].([]any)
					if !ok || len(updates) != 1 || !strings.Contains(updates[0].(string), "fresh update marker") {
						t.Fatalf("counted update is invisible: %v", visible)
					}
				}
			})
		}
	}
}

// The reported unread-count incident no longer reproduced on the live board.
// Acknowledged notifications disappear. Accepted work remains owed and now
// occupies the actionable-first mailbox page; its count must state that
// obligation rather than describe the consumed approval as new mail.
func TestWaitingCountMatchesTheMCPInboxAfterDisposition(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, disposition := range []string{"ack", "approve", "queue"} {
			t.Run(version+"/"+disposition, func(t *testing.T) {
				srv, _ := newServer(t)
				call := func(name string, args map[string]any) map[string]any {
					return waitingCall(t, srv, version, name, args)
				}
				lead := call("register", map[string]any{"name": "lead"})["token"]
				worker := call("register", map[string]any{"name": "worker"})["token"]
				kind := "request"
				if disposition == "ack" {
					kind = "notify"
				}
				sent := call("send", map[string]any{"token": lead, "to": "worker", "type": kind, "body": "count control"})
				if disposition == "ack" {
					call("ack", map[string]any{"token": worker, "msg_serial": sent["msg_serial"]})
				} else {
					call("respond", map[string]any{"token": worker, "msg_serial": sent["msg_serial"], "disposition": disposition})
				}
				read := call("read_mail", map[string]any{"token": worker, "msg_serial": sent["msg_serial"]})
				message := read["message"].(map[string]any)
				state := map[string]string{"ack": "acked", "approve": "approved", "queue": "queued"}[disposition]
				if message["state"] != state || message["consumed"] != true {
					t.Fatalf("setup: disposition did not consume the message: %v", read)
				}
				written := call("update", map[string]any{"token": worker, "description": "count control"})
				waiting, _ := written["waiting"].(string)
				if disposition == "ack" && waiting != "" {
					t.Errorf("acknowledged mail was reported pending: %q", waiting)
				}
				if disposition != "ack" && (!strings.Contains(waiting, "1 owed requests") || strings.Contains(waiting, "1 new")) {
					t.Errorf("accepted obligation was missing or mislabelled: %q", waiting)
				}
				inbox := call("inbox", map[string]any{"token": worker})
				for _, key := range []string{"inbox", "messages"} {
					mail, _ := inbox[key].([]any)
					want := 1
					if disposition == "ack" {
						want = 0
					}
					if len(mail) != want {
						t.Errorf("%s does not match remaining obligations: %v", key, inbox)
					}
				}
				owed, _ := inbox["owed_work"].([]any)
				if disposition == "ack" {
					if len(owed) != 0 {
						t.Fatalf("acknowledged notify became owed work: %v", inbox)
					}
				} else if len(owed) != 1 || owed[0].(map[string]any)["msg_serial"] != sent["msg_serial"] {
					t.Fatalf("accepted request disappeared from owed_work: %v", inbox)
				}
			})
		}
	}
}

func TestWaitingHintClearsTrimmedProgressThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, mixedMail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed-mail=%t", version, mixedMail), func(t *testing.T) {
				srv, _ := newServer(t)
				call := func(name string, args map[string]any) map[string]any {
					return waitingCall(t, srv, version, name, args)
				}
				lead := call("register", map[string]any{"name": "lead"})["token"]
				worker := call("register", map[string]any{"name": "worker"})["token"]
				var parents []float64
				long := strings.Repeat("private-full-progress-marker ", 100)
				for range 2 {
					sent := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "hint probe"})
					parent := sent["msg_serial"].(float64)
					parents = append(parents, parent)
					call("respond", map[string]any{"token": worker, "msg_serial": parent, "disposition": "approve"})
					call("read_mail", map[string]any{"token": lead, "msg_serial": parent})
					// Two updates on one parent must still name just one read call.
					for range 2 {
						call("respond", map[string]any{"token": worker, "msg_serial": parent, "disposition": "progress", "body": long})
					}
				}
				var incoming any
				if mixedMail {
					incoming = call("send", map[string]any{"token": worker, "to": "lead", "type": "handoff", "body": "private incoming marker"})["msg_serial"]
				}
				for _, read := range []string{"inbox", "check_in"} {
					visible := call(read, map[string]any{"token": lead})
					updates, ok := visible["agent_updates"].([]any)
					if !ok || len(updates) == 0 || !strings.Contains(fmt.Sprint(updates), "trimmed") {
						t.Fatalf("setup: %s did not show a trimmed update: %v", read, visible)
					}
				}
				written := call("update", map[string]any{"token": lead, "description": "follow the waiting hint"})
				waiting, _ := written["waiting"].(string)
				if !strings.Contains(waiting, "update(s) to you") {
					t.Fatalf("setup: shortened updates were already cleared: %v", written)
				}
				for _, parent := range parents {
					read := fmt.Sprintf("read_mail(%.0f)", parent)
					if strings.Count(waiting, read) != 1 {
						t.Errorf("waiting must name each parent read once, including updates on the same parent: %q; want %s", waiting, read)
					}
				}
				if !strings.Contains(waiting, "read and clear") {
					t.Errorf("waiting did not explain the full read that clears shortened updates: %q", waiting)
				}
				if strings.Contains(waiting, "inbox") != mixedMail {
					t.Errorf("inbox hint does not match ordinary mail alongside envelope updates: %q", waiting)
				}
				if strings.Contains(waiting, "private") {
					t.Errorf("waiting hint exposed a message body: %q", waiting)
				}
				// Obey the named calls through the same door. Reading one parent
				// must not consume the other; the final full read clears both.
				for i, parent := range parents {
					read := call("read_mail", map[string]any{"token": lead, "msg_serial": parent})
					progress := read["message"].(map[string]any)["progress"].([]any)
					if len(progress) != 2 || progress[1].(map[string]any)["note"] != long {
						t.Fatalf("full parent read did not actually return the complete update: %v", read)
					}
					written = call("update", map[string]any{"token": lead, "description": "full update read"})
					waiting, _ = written["waiting"].(string)
					if i == 0 {
						if !strings.Contains(waiting, "update(s) to you") {
							t.Errorf("reading one parent consumed the other parent's update: %v", written)
						}
					} else if strings.Contains(waiting, "update(s) to you") {
						t.Errorf("obeying the full read hint did not clear the updates: %v", written)
					}
				}
				if mixedMail {
					if !strings.Contains(waiting, "1 seen messages awaiting action") {
						t.Errorf("reading updates consumed unrelated ordinary mail: %v", written)
					}
					call("ack", map[string]any{"token": lead, "msg_serial": incoming})
				}
				written = call("update", map[string]any{"token": lead, "description": "all pending items handled"})
				if waiting, _ := written["waiting"].(string); waiting != "" {
					t.Errorf("waiting did not clear after the instructed reads: %q", waiting)
				}
			})
		}
	}
}

func TestWaitingHintClearsTrimmedReviewThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _ := newServer(t)
			call := func(name string, args map[string]any) map[string]any {
				return waitingCall(t, srv, version, name, args)
			}
			lead := call("register", map[string]any{"name": "lead"})["token"]
			worker := call("register", map[string]any{"name": "worker"})["token"]
			parent := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "review probe", "milestones": []string{"step"}})["msg_serial"].(float64)
			call("respond", map[string]any{"token": worker, "msg_serial": parent, "disposition": "approve"})
			call("respond", map[string]any{"token": worker, "msg_serial": parent, "disposition": "progress", "milestone": 1, "body": "step complete"})
			long := strings.Repeat("private-full-review-marker ", 100)
			call("respond", map[string]any{"token": lead, "msg_serial": parent, "disposition": "accept", "milestone": 1, "body": long})
			visible := call("inbox", map[string]any{"token": worker})
			updates, ok := visible["agent_updates"].([]any)
			if !ok || len(updates) != 1 || !strings.Contains(updates[0].(string), "trimmed") {
				t.Fatalf("setup: actual review was not shortened: %v", visible)
			}
			written := call("update", map[string]any{"token": worker, "description": "read the whole review"})
			waiting, _ := written["waiting"].(string)
			if !strings.Contains(waiting, fmt.Sprintf("read_mail(%.0f)", parent)) || !strings.Contains(waiting, "read and clear") {
				t.Errorf("review reminder did not name the full parent read: %q", waiting)
			}
			read := call("read_mail", map[string]any{"token": worker, "msg_serial": parent})
			progress := read["message"].(map[string]any)["progress"].([]any)
			if progress[len(progress)-1].(map[string]any)["note"] != long {
				t.Fatalf("setup: full review was not returned: %v", read)
			}
			written = call("update", map[string]any{"token": worker, "description": "review read"})
			if waiting, _ := written["waiting"].(string); strings.Contains(waiting, "update(s)") || !strings.Contains(waiting, "1 owed requests") {
				t.Errorf("full review read did not clear the review while retaining owed work: %q", waiting)
			}
		})
	}
}
