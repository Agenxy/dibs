// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestWakeDigestReadIsPrivateNonConsumingAndSessionBound(t *testing.T) {
	srv, _ := newServer(t)
	eng := srv.Config.Handler.(*Server).eng
	const session = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "session_id": session})
	sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})
	token := worker["token"].(string)
	mail := toolCall(t, srv, "send", map[string]any{"token": sender["token"], "to": "worker", "type": "handoff", "body": "fresh-mail-marker"})
	q := toolCall(t, srv, "send", map[string]any{"token": token, "to": "sender", "type": "question", "body": "answer me"})
	toolCall(t, srv, "respond", map[string]any{"token": sender["token"], "msg_serial": q["msg_serial"], "disposition": "answer", "body": "fresh-update-marker"})
	toolCall(t, srv, "hook_poll", map[string]any{"session_id": session, "event": "Stop", "stop_hook_active": true})
	before, err := eng.WakeDigestFor(context.Background(), token, "", "")
	if err != nil || !strings.Contains(before, "agent update") || !strings.Contains(before, "fresh-mail-marker") {
		t.Fatalf("setup: missing actual notice and pending mail: %q (%v)", before, err)
	}
	board, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	read := func(tok, sid string) map[string]any {
		return rpc(t, srv, "2026-07-28", "resources/read", map[string]any{
			"uri":   WakeDigestURI,
			"_meta": map[string]any{metaTokenKey: tok, SessionMetaKey: sid},
		})
	}
	for range 2 {
		out := result(t, read(token, session), "fresh wake digest")
		if out["cacheScope"] != scopePrivate || out["ttlMs"] != float64(0) {
			t.Fatalf("unsafe cache hints: %v", out)
		}
		text := out["contents"].([]any)[0].(map[string]any)["text"].(string)
		if text != before {
			t.Fatalf("fresh read consumed mail or notices: %q != %q", text, before)
		}
	}
	after, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after["serial"] != board["serial"] {
		t.Fatalf("fresh read changed replayable state: %v -> %v", board["serial"], after["serial"])
	}
	still, err := eng.WakeDigestFor(context.Background(), token, "", "")
	if err != nil || still != before {
		t.Fatalf("fresh read drained the notice queue: %q (%v)", still, err)
	}
	peek, err := eng.AllMessages(context.Background()) // admin snapshot, NOT read_mail (which marks delivery)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range peek["messages"].([]*core.Message) {
		if float64(message.Serial) != mail["msg_serial"] {
			continue
		}
		found = true
		if message.State != core.MsgStatePending || message.DeliveredAt != 0 {
			t.Fatalf("a refresh spent mail delivery: %+v", message)
		}
	}
	if !found {
		t.Fatal("setup: pending mail disappeared")
	}
	toolCall(t, srv, "ack", map[string]any{"token": token, "msg_serial": mail["msg_serial"]})
	toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": q["msg_serial"]})
	cleared := result(t, read(token, session), "acknowledged mail and read update")
	if text := cleared["contents"].([]any)[0].(map[string]any)["text"]; text != "" {
		t.Fatalf("handled mail or a read agent update remained in the fresh digest: %v", text)
	}
	wrong := result(t, read(token, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), "moved session")
	if text := wrong["contents"].([]any)[0].(map[string]any)["text"]; text != "" {
		t.Fatalf("another session received mail: %v", text)
	}
	for _, tc := range [][2]string{{"", session}, {token, ""}, {"wrong-token", session}} {
		if out := read(tc[0], tc[1]); out["error"] == nil {
			t.Fatalf("missing authentication/binding accepted: %v", out)
		}
	}
	listed := result(t, rpc(t, srv, "2026-07-28", "resources/list", map[string]any{}), "resources")
	for _, resource := range listed["resources"].([]any) {
		if resource.(map[string]any)["uri"] == WakeDigestURI {
			t.Fatal("bridge-only digest leaked into resource discovery")
		}
	}
}
