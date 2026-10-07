// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The payload must carry only what board_app.html draws. It lives in the App's
// private metadata rather than model context, but it still rides every panel
// result; invisible transport is not permission for unbounded transport.
func TestPanelPayloadCarriesOnlyRenderedFields(t *testing.T) {
	in := core.Result{
		"agent_id": "opus-5",
		"view":     "mail",
		"board": core.Result{
			"node": "n1", "serial": 42,
			"agents": []core.Result{{
				"id": "opus-5", "name": "opus-5", "kind": "ephemeral", "status": "active",
				"description": "d", "last_coordination_at": "t", "agent": map[string]any{"model": "m"},
				// none of these are drawn:
				"activation": 3, "acked_serial": 7, "proc_alive": true, "last_seen": "t", "pid": 999,
				"slots": []core.Result{{
					"id": "s1", "text": "w", "refs": []string{"r"},
					"updated_serial": 12,
				}},
			}},
		},
		"inbox": core.Result{"messages": []core.Result{{
			"serial": 1, "type": "question", "from": "a", "to": "b", "body": "x",
			"deadline": "t", "delivered_serial": 4, "consumed": false,
		}}},
		"acked_serial": 9, "ok": true, "truncated_before_serial": 0,
	}
	out := panelPayload(in)
	blob, _ := json.Marshal(out)
	s := string(blob)

	for _, leaked := range []string{
		"activation", "acked_serial", "proc_alive", "last_seen",
		"pid", "updated_serial", "deadline", "delivered_serial", "consumed", "truncated_before_serial",
	} {
		if strings.Contains(s, leaked) {
			t.Errorf("payload leaks %q: it is not drawn by the panel", leaked)
		}
	}
	for _, needed := range []string{"opus-5", "active", "question", "agent_id", "view"} {
		if !strings.Contains(s, needed) {
			t.Errorf("payload dropped %q, which the panel renders", needed)
		}
	}
	// Trimming must not silently empty the board: the failure that shipped once
	// was a core.Result assertion against a plain map, which dropped every agent.
	b := asMap(out["board"])
	if b == nil || len(asMaps(b["agents"])) != 1 {
		t.Fatal("board lost its agents in trimming")
	}
	// Same payload, but with the board as a bare map: the shape the engine
	// returns through check_in.
	in2 := core.Result{"board": map[string]any{"agents": []any{
		map[string]any{"id": "x", "status": "active"},
	}}}
	if b2 := asMap(panelPayload(in2)["board"]); b2 == nil || len(asMaps(b2["agents"])) != 1 {
		t.Fatal("bare map[string]any board was dropped")
	}
}

func TestPanelKeepsContactAlertsWithoutMessageBodies(t *testing.T) {
	in := core.Result{"board": core.Result{
		"agents": []core.Result{},
		"contact_alerts": []core.Result{{
			"serial": uint64(19), "recipient": "worker", "oldest_serial": uint64(12),
			"high_water": uint64(14), "count": 3,
		}},
	}}
	out := panelPayload(in)
	board := asMap(out["board"])
	if board == nil {
		t.Fatal("panel lost board")
	}
	alerts := asMaps(board["contact_alerts"])
	if len(alerts) != 1 || alerts[0]["recipient"] != "worker" {
		t.Fatalf("panel dropped contact metadata: %v", board["contact_alerts"])
	}
	if _, ok := alerts[0]["body"]; ok {
		t.Fatal("contact alert exposed a participant body")
	}
}

func TestPanelShowsDeliveryStartWindowOnlyForNewMessages(t *testing.T) {
	in := core.Result{"inbox": core.Result{"messages": []core.Result{
		{"serial": 1, "state": core.MsgStatePending, "response_window_s": 600},
		{
			"serial": 2, "state": core.MsgStateDelivered, "response_window_s": 600,
			"deadline": "2026-10-06T12:00:00Z",
		},
		{"serial": 3, "state": core.MsgStatePending, "deadline": "old-send-deadline"},
	}}}
	views := asMaps(panelPayload(in)["inbox"])
	if len(views) != 3 {
		t.Fatalf("mail lost in panel trim: %v", views)
	}
	if views[0]["response_window_s"] != float64(600) || views[0]["deadline"] != nil ||
		views[1]["deadline"] != "2026-10-06T12:00:00Z" {
		t.Fatalf("delivery-start clock not represented honestly: %v", views)
	}
	if _, ok := views[2]["deadline"]; ok {
		t.Fatalf("legacy field added despite not being rendered: %v", views[2])
	}
}

// The model-facing summary counts from the FULL result, not the trimmed one,
// otherwise trimming would silently change what the model is told.
func TestSummaryCountsSurviveTrimming(t *testing.T) {
	res := core.Result{
		"board": core.Result{"agents": []core.Result{
			{"id": "a", "status": "active"}, {"id": "b", "status": "dormant"},
		}},
		"inbox": core.Result{"messages": []core.Result{{"serial": 1}, {"serial": 2}}},
	}
	text := showBoardResult(res, false, false)["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"2 agent(s)", "1 active", "2 unread"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary %q lost %q", text, want)
		}
	}
}

// A host that drops tool-result _meta must still be able to fill the panel.
//
// This is the test that did not exist when it was needed. The panel's data
// travels in _meta, which is correct and keeps the board out of model context,
// and a host that forwards none of it left the panel on "awaiting board · No
// agents yet" while the daemon held three agents. Nothing failed: `content` was a
// correct 72-character summary the whole time, every assertion about the tool
// result passed, and the only way to see it was to look at the panel.
//
// So the property under test is not "the payload is in _meta": that passed
// throughout, but "the panel has a route to the board that does not depend on
// the host honouring _meta". That route is the bootstrap: a token, and a tool
// the panel can spend it on. Both halves are asserted here, because either one
// alone is again a panel that quietly shows nothing.
func TestPanelCanReachTheBoardOnAHostThatDropsMeta(t *testing.T) {
	srv, _ := newServer(t)
	const marker = "BOARD DETAIL THE BOOTSTRAP MUST NOT CARRY"
	registered := toolCall(t, srv, "register", map[string]any{
		"name": "panel-bootstrap", "description": marker,
	})
	token := registered["token"].(string)

	// What the panel receives when the host forwards content + structuredContent
	// and drops _meta entirely.
	result := rawToolResult(t, srv, "board", map[string]any{"token": token})
	boot, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatal("no bootstrap: a host that drops _meta leaves the panel with no way to fetch")
	}
	got, _ := boot["act_token"].(string)
	if got != token {
		t.Fatalf("bootstrap act_token = %q, want the caller's own token", got)
	}
	// The token is the caller's own, so it is not new information reaching the
	// model, but the board would be, and that is the cost board promises
	// not to charge.
	blob, _ := json.Marshal(boot)
	if strings.Contains(string(blob), marker) {
		t.Errorf("bootstrap carries board detail: %s", blob)
	}

	// The other half: the call the panel makes with that token has to answer with
	// the board in ordinary content, since content is the one field every host
	// forwards to the app.
	fetched := rawToolResult(t, srv, "board", map[string]any{
		"token": token, "detail": true,
	})
	text, _ := fetched["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("panel's fetch did not return JSON it can draw: %v", err)
	}
	if _, has := payload["board"]; !has {
		t.Fatal("panel's fetch returned no board; the fallback route is dead")
	}
}

func TestQueuedPanelCarriesItsRenderedDeadlineAndOrder(t *testing.T) {
	out := panelPayload(core.Result{"inbox": core.Result{"messages": []core.Result{{
		"serial": 7, "type": "request", "state": core.MsgStateQueued, "deadline": "2030-01-01T00:00:00Z",
		"queue_rank": 2, "request_priority": "high", "queue_order_locked": true, "queue_by": "worker",
	}}}})
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{"deadline", "queue_rank", "request_priority", "queue_order_locked", "queue_by"} {
		if !strings.Contains(string(encoded), rendered) {
			t.Errorf("panel dropped rendered queued field %s", rendered)
		}
	}
}
