package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// toolCallWithMeta is toolCall with the harness's own _meta on the call, as
// Codex attaches `threadId` to every tools/call.
func toolCallWithMeta(t *testing.T, srv *httptest.Server, name string, args, meta map[string]any) map[string]any {
	t.Helper()
	out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{"name": name, "arguments": args, "_meta": meta})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %v", name, out)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	_ = json.Unmarshal([]byte(content), &payload)
	return payload
}

// A stream following the board alone answers to no credential. The
// standing check applied to it looked the empty token up, found no agent,
// and closed the stream on its first notification.
func TestABoardOnlySubscriptionSurvivesItsFirstNotification(t *testing.T) {
	srv, _ := newServer(t)
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "listen", "method": "subscriptions/listen",
		"params": map[string]any{
			"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://board"}},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listen did not open: %s", resp.Status)
	}
	lines := scanLines(ctx, resp)
	toolCall(t, srv, "register", map[string]any{"name": "newcomer", "cwd": t.TempDir()})
	if !awaitUpdate(lines, "dibs://board") {
		t.Fatal("a board-only subscription was handed nothing on the first board change: " +
			"the stream closed on a credential it never had")
	}
	toolCall(t, srv, "register", map[string]any{"name": "another", "cwd": t.TempDir()})
	if !awaitUpdate(lines, "dibs://board") {
		t.Fatal("the board-only subscription did not survive its first notification")
	}
}

// The row retains every thread it has been bound to, so "holds" alone let a
// stream serving thread A go on delivering after the hooks had moved the
// agent to thread B. A stated thread is held only while it is the current
// session.
func TestAStreamServingAThreadIsWithheldOnceTheAgentMovesToAnother(t *testing.T) {
	srv, _ := newServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	nonce := "n-busy-0123456789abcdef"
	threadA := "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b"
	threadB := "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7c"
	busy := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-codex", "cwd": t.TempDir(),
	}, map[string]any{"threadId": threadA})
	token, _ := busy["token"].(string)
	if token == "" {
		t.Fatalf("setup: %v", busy)
	}
	inA := openListenFrom(t, srv, token, threadA, nil)
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "in A"})
	if !awaitUpdate(inA, "dibs://inbox") {
		t.Fatal("setup: the stream serving the agent's own thread heard nothing")
	}
	// The same bridge process, another thread: the row keeps A and moves
	// to B.
	moved := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-codex", "cwd": t.TempDir(),
	}, map[string]any{"threadId": threadB})
	if moved["agent_id"] != busy["agent_id"] {
		t.Fatalf("setup: the move forked a sibling: %v", moved)
	}
	// The move ROTATES the credential (round seventy-nine), so everything after
	// it speaks with the new one. That is deliberate here: this test is about
	// the SESSION rule, and using a revoked token would prove the credential
	// rule instead and leave the session rule untested.
	movedTok, _ := moved["token"].(string)
	if movedTok == "" {
		t.Fatalf("setup: the move issued no token: %v", moved)
	}
	inB := openListenFrom(t, srv, movedTok, threadB, nil)
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "in B"})
	if !awaitUpdate(inB, "dibs://inbox") {
		t.Fatal("the stream serving the thread the agent moved to heard nothing")
	}
	// A stream opened with the CURRENT credential, naming the thread the agent
	// has left, is the isolated form of the rule: the token is unimpeachable
	// and the session is not the one the agent is in.
	staleA := openListenFrom(t, srv, movedTok, threadA, nil)
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "still in B"})
	if awaitUpdate(staleA, "dibs://inbox") {
		t.Fatal("a stream naming the thread the agent LEFT, on a perfectly valid credential, " +
			"was handed the question: the row retains the thread, and retaining it is not " +
			"being in it")
	}
}
