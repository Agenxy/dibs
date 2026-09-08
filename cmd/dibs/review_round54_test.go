package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// A bridge serves every agent that registers through it, and the watcher
// held one token: registering a second agent retired the first one's
// subscription, so only the last registered mailbox kept its self-wake. One
// stream per agent; a rotated token replaces only its own agent's stream.
func TestEveryAgentRegisteredThroughOneBridgeKeepsItsSelfWake(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams() // the handoff record is process-wide, and other tests leave a token in it
	t.Cleanup(resetWakeStreams)
	var mu sync.Mutex
	subscribed := map[string]int{} // token -> streams opened
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tok, _ := req.Params.Meta["com.dibs/token"].(string)
		mu.Lock()
		subscribed[tok]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-hold
	}))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher
	hook := watchOnRegister(ctx, &iw, srv.Client(), srv.URL, "secret")
	sent := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{}}}`)
	reply := func(agent, token string, serial int) []byte {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent_id\":\"%s\",\"token\":\"%s\",\"serial\":%d}"}]}}`, agent, token, serial))
	}
	hook(sent, reply("first", "tok-first", 5))
	hook(sent, reply("second", "tok-second", 7))
	if got := iw.tokens(); !slices.Equal(got, []string{"tok-first", "tok-second"}) {
		t.Fatalf("after two agents registered through one bridge the watcher holds %v: the first one's "+
			"self-wake was retired by the second's registration", got)
	}
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(subscribed)
		mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the daemon saw subscriptions for %d token(s), want both", n)
		case <-time.After(20 * time.Millisecond):
		}
	}
	// A rotation replaces its own agent's stream and keeps the other.
	hook(sent, reply("first", "tok-first-2", 9))
	if got := iw.tokens(); !slices.Equal(got, []string{"tok-first-2", "tok-second"}) {
		t.Fatalf("after the first agent's token rotated the watcher holds %v", got)
	}
	if since := iw.sinceOf("tok-first-2"); since != 5 {
		t.Fatalf("the rotated stream's cursor is %d, want the established 5", since)
	}
	// And the handoff carries both.
	streams := handoffState().WakeStreams
	var tokens []string
	for _, st := range streams {
		tokens = append(tokens, st.Token)
	}
	if !slices.Equal(tokens, []string{"tok-first-2", "tok-second"}) {
		t.Fatalf("the handoff carries %v", tokens)
	}
}
