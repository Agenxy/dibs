package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// openListenFrom is openListen with the session the stream serves, as the
// stdio bridge attaches it.
func openListenFrom(t *testing.T, srv *httptest.Server, token, session string, since any) <-chan string {
	t.Helper()
	meta := map[string]any{metaTokenKey: token, SessionMetaKey: session}
	if since != nil {
		meta[SinceMetaKey] = since
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "listen", "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta":         meta,
			"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://inbox"}},
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
	return scanLines(ctx, resp)
}

// A subscription captured its agent when it opened and went on delivering
// after the token was rotated away, so whoever still held the old stream
// read the new holder's mail. The stream ends with the credential it was
// opened with.
func TestASubscriptionEndsWhenItsTokenIsRotatedAway(t *testing.T) {
	srv, _ := newServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "session_id": "host-1", "cwd": t.TempDir()})
	old := busy["token"].(string)
	lines := openListen(t, srv, old, nil)
	// A same-name, same-session register with no nonce reattaches and
	// rotates the token: the old one is dead.
	again := toolCall(t, srv, "register", map[string]any{"name": "busy", "session_id": "host-1", "cwd": t.TempDir()})
	if again["token"] == nil || again["token"] == old || again["agent_id"] != busy["agent_id"] {
		t.Fatalf("setup: the register did not rotate the token in place: %v", again)
	}
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "after the rotation"})
	if awaitUpdate(lines, "dibs://inbox") {
		t.Fatal("the stream opened with the rotated-away token delivered the question")
	}
	// And it ENDS rather than going quiet, so nothing behind it wakes anybody
	// again.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-lines:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("the stream opened with the rotated-away token stayed open")
		}
	}
}

// A bridge left behind by an identity that moved to another session kept
// waking the session the agent had left, which the daemon's own wake routes
// never do. The daemon withholds the inbox from a stream whose agent is
// elsewhere, and hands it back when the agent returns.
func TestAStreamFollowsItsAgentOnlyWhileTheAgentIsInItsSession(t *testing.T) {
	srv, _ := newServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	nonce := "n-busy-0123456789abcdef"
	busy := toolCall(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-a", "cwd": t.TempDir(),
	})
	token := busy["token"].(string)
	inA := openListenFrom(t, srv, token, "host-a", nil)
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "while in A"})
	if !awaitUpdate(inA, "dibs://inbox") {
		t.Fatal("setup: the stream served from the agent's own session heard nothing")
	}
	// The identity moves to session B with its nonce, inside the TTL: the
	// token is kept, the session is not.
	moved := toolCall(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-b", "cwd": t.TempDir(),
	})
	if moved["agent_id"] != busy["agent_id"] || moved["token"] != token {
		t.Fatalf("setup: the move did not keep the identity and its token: %v", moved)
	}
	inB := openListenFrom(t, srv, token, "host-b", nil)
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "while in B"})
	if !awaitUpdate(inB, "dibs://inbox") {
		t.Fatal("the stream served from the session the agent moved to heard nothing")
	}
	if awaitUpdate(inA, "dibs://inbox") {
		t.Fatal("the stream served from the session the agent LEFT was handed the question: " +
			"the bridge there wakes a session the agent is not in")
	}
	// A return to A is a move back: A's stream, still open, is fed again.
	toolCall(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-a", "cwd": t.TempDir(),
	})
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "back in A"})
	if !awaitUpdate(inA, "dibs://inbox") {
		t.Fatal("the agent returned to A and A's stream was not fed again")
	}
}

// The gap too: a left-behind bridge that reconnected with a cursor, after
// a daemon restart, was handed the mail it had missed and woke on it.
func TestAResumedStreamIsNotHandedTheGapWhileItsAgentIsElsewhere(t *testing.T) {
	srv, _ := newServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	nonce := "n-busy-0123456789abcdef"
	busy := toolCall(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-a", "cwd": t.TempDir(),
	})
	token := busy["token"].(string)
	cursor := busy["serial"]
	toolCall(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-b", "cwd": t.TempDir(),
	})
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "while in B"})
	// A's bridge reconnects from where it left off.
	inA := openListenFrom(t, srv, token, "host-a", cursor)
	if awaitUpdate(inA, "dibs://inbox") {
		t.Fatal("the stream served from the session the agent LEFT was handed the gap on reconnect")
	}
	// B's is.
	inB := openListenFrom(t, srv, token, "host-b", cursor)
	if !awaitUpdate(inB, "dibs://inbox") {
		t.Fatal("the stream served from the session the agent is in was not handed the gap")
	}
}
