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
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/ledger"
)

func restartableQueueServer(t *testing.T, dir string) (*httptest.Server, *engine.Engine, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("fixture", core.DefaultLimits())
	if _, err = journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, journal, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	server := New(eng)
	server.SetTaskKey("fixture-task-secret")
	srv := httptest.NewServer(server)
	var once sync.Once
	closeFixture := func() {
		once.Do(func() {
			srv.Close()
			cancel()
			<-done
			if err := journal.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(closeFixture)
	return srv, eng, closeFixture
}

func TestQueueRestartsAsDebtAndTrackedTaskThroughMCP(t *testing.T) {
	dir := t.TempDir()
	srv, _, stop := restartableQueueServer(t, dir)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead", "nonce": "queue-lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "nonce": "queue-worker"})["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	out := rpc(t, srv, "2026-07-28", "tools/call", withTasks(map[string]any{"token": lead, "to": "worker", "type": "request", "body": "private payload", "priority": "high", "track": true}))["result"].(map[string]any)
	n := out["_meta"].(map[string]any)["com.dibs/msg_serial"].(float64)
	id := out["taskId"].(string)
	queued := toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "queue"})
	if queued["state"] != "queued" {
		t.Fatalf("setup: %v", queued)
	}
	// Same stable task key on both actual servers, as dibd uses its board secret.
	before := toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
	if before["message"].(map[string]any)["queue_debt"] != true {
		t.Fatalf("debt not recorded: %v", before)
	}
	tracked := rpc(t, srv, "2026-07-28", "tasks/get", taskCapabilities(map[string]any{"taskId": id}))["result"].(map[string]any)
	if tracked["status"] != "working" || tracked["statusMessage"] != "queued #1" {
		t.Fatalf("queued mapping: %v", tracked)
	}
	stop()
	srv, _, _ = restartableQueueServer(t, dir)
	after := toolCall(t, srv, "check_in", map[string]any{"token": worker})
	q, ok := after["task_queue"].([]any)
	if !ok || len(q) != 1 {
		t.Fatalf("lost queue across replay: %v", after)
	}
	read := toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
	if read["queue_position"] != float64(1) || read["message"].(map[string]any)["queue_debt"] != true {
		t.Fatalf("replayed debt/position: %v", read)
	}
	tracked = rpc(t, srv, "2026-07-28", "tasks/get", taskCapabilities(map[string]any{"taskId": id}))["result"].(map[string]any)
	if tracked["statusMessage"] != "queued #1" {
		t.Fatalf("task handle lost on restart: %v", tracked)
	}
}

func TestQueueLocksThroughAuthenticatedMCP(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker"})["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	var ids []float64
	for range 3 {
		n := toolCall(t, srv, "send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "private"})["msg_serial"].(float64)
		r := toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "queue"})
		if r["state"] != "queued" {
			t.Fatalf("setup: %v", r)
		}
		ids = append(ids, n)
	}
	r := toolCall(t, srv, "queue_lock", map[string]any{"token": worker, "agent": "worker", "msg_serial": ids[1], "locked": true})
	if r["__is_error"] != true {
		t.Fatalf("ordinary actor locked policy: %v", r)
	}
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator}); err != nil {
		t.Fatal(err)
	}
	r = toolCall(t, srv, "queue_lock", map[string]any{"token": lead, "agent": "worker", "msg_serial": ids[1], "locked": true})
	if r["held"] != true {
		t.Fatalf("lock: %v", r)
	}
	r = toolCall(t, srv, "queue_update", map[string]any{"token": worker, "msg_serial": ids[2], "before": ids[0]})
	if r["__is_error"] != true {
		t.Fatalf("crossed task lock: %v", r)
	}
	r = toolCall(t, srv, "queue_lock", map[string]any{"token": lead, "agent": "worker", "locked": true})
	if r["held"] != true {
		t.Fatalf("queue lock: %v", r)
	}
	r = toolCall(t, srv, "queue_update", map[string]any{"token": worker, "msg_serial": ids[0], "priority": "urgent"})
	if r["__is_error"] != true {
		t.Fatalf("ignored whole queue lock: %v", r)
	}
	r = toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": ids[1], "disposition": "approve"})
	if r["state"] != "approved" {
		t.Fatalf("lock forced or blocked starting: %v", r)
	}
	r = toolCall(t, srv, "queue_lock", map[string]any{"token": lead, "agent": "worker", "locked": false})
	if r["held"] != false {
		t.Fatalf("revoke: %v", r)
	}
	r = toolCall(t, srv, "queue_update", map[string]any{"token": worker, "msg_serial": ids[2], "before": ids[0]})
	if r["changed"] != true {
		t.Fatalf("unlocked move: %v", r)
	}
}

// Every call enters through the MCP handler; ignored parameters must fail here.
func TestRequestQueueThroughMCP(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker"})["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	send := func(priority string) float64 {
		t.Helper()
		r := toolCall(t, srv, "send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "private work", "priority": priority})
		n, ok := r["msg_serial"].(float64)
		if !ok {
			t.Fatalf("send setup: %v", r)
		}
		return n
	}
	normal, urgent := send("normal"), send("urgent")
	for _, n := range []float64{normal, urgent} {
		r := toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "queue"})
		if r["state"] != "queued" {
			t.Fatalf("queue did not accept owed work: %v", r)
		}
	}
	read := func(n float64) map[string]any {
		return toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
	}
	if r := read(urgent); r["queue_position"] != float64(1) {
		t.Fatalf("urgent position: %v", r)
	}
	if r := read(normal); r["queue_position"] != float64(2) {
		t.Fatalf("normal position: %v", r)
	}
	r := toolCall(t, srv, "queue_update", map[string]any{"token": worker, "msg_serial": normal, "before": urgent})
	if r["queue_position"] != float64(1) {
		t.Fatalf("manual order: %v", r)
	}
	r = toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": normal, "disposition": "approve"})
	if r["state"] != "approved" {
		t.Fatalf("start: %v", r)
	}
	r = toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": normal, "disposition": "done"})
	if r["state"] != "done" {
		t.Fatalf("done: %v", r)
	}
	q, ok := r["task_queue"].([]any)
	if !ok || len(q) != 1 || q[0].(map[string]any)["msg_serial"] != urgent {
		t.Fatalf("done did not return the remaining ordered queue: %v", r)
	}
	if owed, ok := r["owed_work"].([]any); !ok || len(owed) != 1 ||
		!strings.Contains(fmt.Sprint(owed[0]), "when you choose to start") {
		t.Fatalf("done omitted the literal next-work call: %v", r)
	}
	if r := read(urgent); r["message"].(map[string]any)["state"] != "queued" {
		t.Fatalf("done started next request: %v", r)
	}
}

func TestHumanHandoffHasNoAgentWakeFailureThroughMCP(t *testing.T) {
	for _, route := range []string{"desktop", "relay"} {
		t.Run(route, func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			eng.SetHumanNotifier(desktopFixture{available: route == "desktop", ask: func(humanask.Message) (humanask.Answer, error) { <-release; return humanask.Answer{}, nil }})
			if route == "relay" {
				_, detach := eng.AttachHumanRelay()
				t.Cleanup(detach)
			}
			token := toolCall(t, srv, "register", map[string]any{"name": "sender"})["token"].(string)
			r := toolCall(t, srv, "send", map[string]any{"token": token, "to": "human", "type": "notify", "body": "receipt"})
			if r["human_route"] != route {
				t.Fatalf("setup route: %v", r)
			}
			note, _ := r["note"].(string)
			if strings.Contains(note, "nothing") || strings.Contains(note, "starts that agent") {
				t.Fatalf("human handoff got agent-wake failure: %v", r)
			}
		})
	}
}

func TestQueueOrderingNewsIsRebuiltUntilRead(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := restartableQueueServer(t, dir)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead", "nonce": "queue-news-lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "nonce": "queue-news-worker"})["token"].(string)
	send := func(priority string) float64 {
		t.Helper()
		r := toolCall(t, srv, "send", map[string]any{
			"token": lead, "to": "worker", "type": "request", "body": "secret queue body", "priority": priority,
		})
		n, ok := r["msg_serial"].(float64)
		if !ok {
			t.Fatalf("send setup failed: %v", r)
		}
		r = toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "queue"})
		if r["state"] != "queued" {
			t.Fatalf("queue setup failed: %v", r)
		}
		return n
	}
	first := send("normal")
	toolCall(t, srv, "read_mail", map[string]any{"token": lead, "msg_serial": first})
	second := send("urgent") // insertion changes the first request's position
	toolCall(t, srv, "read_mail", map[string]any{"token": lead, "msg_serial": second})
	board, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret queue body") {
		t.Fatal("queue body leaked into public board")
	}
	stop()
	srv, _, stop = restartableQueueServer(t, dir)
	notices := toolCall(t, srv, "inbox", map[string]any{"token": lead})["agent_updates"]
	if !strings.Contains(fmt.Sprint(notices), "Queue position or priority changed") {
		t.Fatalf("lost unread ordering news on replay: %v", notices)
	}
	toolCall(t, srv, "read_mail", map[string]any{"token": lead, "msg_serial": first})
	stop()
	srv, _, _ = restartableQueueServer(t, dir)
	notices = toolCall(t, srv, "inbox", map[string]any{"token": lead})["agent_updates"]
	if strings.Contains(fmt.Sprint(notices), "Queue position or priority changed") {
		t.Fatalf("read ordering news duplicated on replay: %v", notices)
	}
}

func TestQueueMovesWithTheMailboxAndProjectsLiveRanks(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	lost := toolCall(t, srv, "register", map[string]any{"name": "lost"})["token"].(string)
	heir := toolCall(t, srv, "register", map[string]any{"name": "heir"})["token"].(string)
	send := func(to, token string) float64 {
		t.Helper()
		mail := toolCall(t, srv, "send", map[string]any{
			"token": lead, "to": to, "type": "request", "body": "private transferred task",
		})
		n, ok := mail["msg_serial"].(float64)
		if !ok {
			t.Fatalf("send setup: %v", mail)
		}
		res := toolCall(t, srv, "respond", map[string]any{"token": token, "msg_serial": n, "disposition": "queue"})
		if res["state"] != "queued" {
			t.Fatalf("queue setup: %v", res)
		}
		return n
	}
	first, second := send("lost", lost), send("heir", heir)
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"lost"}}); err != nil {
		t.Fatal(err)
	}
	if res := toolCall(t, srv, "adopt_agent", map[string]any{"token": heir, "agent": "lost"}); res["__is_error"] != true || res["code"] != "E_NOT_PERMITTED" {
		t.Fatalf("ordinary actor adopted another queue: %v", res)
	}
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpGrantRole, To: "heir", Mode: core.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	res := toolCall(t, srv, "adopt_agent", map[string]any{"token": heir, "agent": "lost"})
	if res["__is_error"] == true {
		t.Fatalf("authorized mailbox move failed: %v", res)
	}
	for i, serial := range []float64{first, second} {
		read := toolCall(t, srv, "read_mail", map[string]any{"token": heir, "msg_serial": serial})
		message, ok := read["message"].(map[string]any)
		if !ok || message["to"] != "heir" || message["queue_rank"] != float64(i+1) {
			t.Fatalf("moved queue rank/owner stale: %v", read)
		}
	}
	if read := toolCall(t, srv, "read_mail", map[string]any{"token": lost, "msg_serial": first}); read["__is_error"] != true {
		t.Fatalf("old mailbox still reads transferred queued body: %v", read)
	}
}

func TestQueuedCheckpointReportsOverdueWithoutExpiringOrStartingWork(t *testing.T) {
	srv, _, _ := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker"})["token"].(string)
	mail := toolCall(t, srv, "send", map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "later", "deadline_s": 1,
	})
	n, ok := mail["msg_serial"].(float64)
	if !ok {
		t.Fatalf("send setup: %v", mail)
	}
	if mail["deadline_pending"] != true || mail["response_window_s"] != float64(1) || mail["deadline"] != nil {
		t.Fatalf("send did not state the delivery-start clock honestly: %v", mail)
	}
	res := toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "queue"})
	if res["state"] != "queued" {
		t.Fatalf("queue setup: %v", res)
	}
	<-time.After(1100 * time.Millisecond)
	res = toolCall(t, srv, "check_in", map[string]any{"token": worker})
	q, ok := res["task_queue"].([]any)
	if !ok || len(q) != 1 {
		t.Fatalf("overdue queue lost: %v", res)
	}
	item := q[0].(map[string]any)
	age, ok := item["overdue_s"].(float64)
	if item["overdue"] != true || !ok || age <= 0 {
		t.Fatalf("checkpoint hides deadline age: %v", item)
	}
	read := toolCall(t, srv, "read_mail", map[string]any{"token": worker, "msg_serial": n})
	if read["message"].(map[string]any)["state"] != "queued" || read["overdue"] != true {
		t.Fatalf("deadline expired or started accepted work: %v", read)
	}
}
