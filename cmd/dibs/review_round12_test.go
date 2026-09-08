package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A reconnect carries the serial of the last notification seen, so the daemon
// replays what arrived while the stream was down.
func TestAReconnectingWatcherSaysWhereItLeftOff(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	bodies := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		bodies <- req
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox","_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"notify","com.dibs/serial":7}}}`+"\n\n")
		fl.Flush()
		// then the stream ends, and the watcher reconnects
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := inboxWatcher{reconnect: 50 * time.Millisecond}
	w.start(ctx, srv.Client(), srv.URL, "local-secret", "agent-token")
	since := func(req map[string]any) any {
		params, _ := req["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		return meta["com.dibs/since"]
	}
	first := <-bodies
	if since(first) != nil {
		t.Fatalf("the first subscription claims a cursor it cannot have: %v", since(first))
	}
	select {
	case second := <-bodies:
		if got, _ := since(second).(float64); got != 7 {
			t.Fatalf("the reconnect carries since=%v, want 7: every message that arrived while "+
				"the stream was down produces no notification", since(second))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher did not reconnect")
	}
}
