// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func openNamedSelfWake(t *testing.T, srv *httptest.Server, token, claim string) func() {
	t.Helper()
	const session = "71f97290-0001-4000-8000-555555555555"
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "claim", "method": "subscriptions/listen", "params": map[string]any{
		"_meta":         map[string]any{metaTokenKey: token, SessionMetaKey: session, SelfWakeMetaKey: true, "com.dibs/self_wake_claim": claim},
		"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://inbox"}},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	closeStream := func() { cancel(); _ = resp.Body.Close() }
	t.Cleanup(closeStream)
	if !awaitAck(scanLines(ctx, resp)) {
		t.Fatal("setup: named subscription was not acknowledged")
	}
	return closeStream
}

// Enter through subscriptions/listen and resources/read. Named handoff is
// private to the current token/session, does not consume coordination state,
// and neither an old stream close nor a repeated release clears a newer claim.
func TestSelfWakeReleaseIsAuthenticatedIdempotentAndFencedToItsOwnClaim(t *testing.T) {
	srv, _ := newServer(t)
	eng := srv.Config.Handler.(*Server).eng
	const session = "71f97290-0001-4000-8000-555555555555"
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker", "session_id": session})
	other := toolCall(t, srv, "register", map[string]any{"name": "other"})
	token := worker["token"].(string)
	first := openNamedSelfWake(t, srv, token, "route-a")
	second := openNamedSelfWake(t, srv, token, "route-a")
	openNamedSelfWake(t, srv, token, "route-b")
	before, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release := func(tok, sid, claim string) map[string]any {
		return rpc(t, srv, "2026-07-28", "resources/read", map[string]any{
			"uri": WakeDigestURI, "_meta": map[string]any{metaTokenKey: tok, SessionMetaKey: sid, "com.dibs/self_wake_release": claim},
		})
	}
	if out := release("bad-token", session, "route-b"); out["error"] == nil {
		t.Fatal("bad token accepted", out)
	}
	for _, args := range [][3]string{{other["token"].(string), session, "route-b"}, {token, "wrong-session", "route-b"}, {token, session, "unknown-route"}} {
		release(args[0], args[1], args[2])
		if live, _ := eng.SelfWaking("worker"); !live {
			t.Fatal("another token, session or claim cleared the route", args)
		}
	}
	release(token, session, "route-a")
	first()
	second()
	if live, _ := eng.SelfWaking("worker"); !live {
		t.Fatal("old release/stream close cleared the newer bridge")
	}
	out := result(t, release(token, session, "route-b"), "named handoff")
	meta, _ := out["_meta"].(map[string]any)
	if meta["com.dibs/self_wake_release"] != "route-b" {
		t.Fatal("daemon did not acknowledge named release", out)
	}
	if live, _ := eng.SelfWaking("worker"); live {
		t.Fatal("explicit release left its subscription claim live")
	}
	openNamedSelfWake(t, srv, token, "route-c")
	release(token, session, "route-b")
	if live, _ := eng.SelfWaking("worker"); !live {
		t.Fatal("repeated release cleared a newer route")
	}
	release(token, session, "route-c")
	if live, _ := eng.SelfWaking("worker"); live {
		t.Fatal("new route not released")
	}
	after, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before["serial"] != after["serial"] {
		t.Fatal("claim handoff changed replayable state", before["serial"], after["serial"])
	}
}
