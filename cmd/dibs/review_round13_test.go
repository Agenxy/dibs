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

// A stream that drops on an empty inbox reconnects with the cursor the
// acknowledgment gave it, not blind.
func TestAWatcherThatSawNoMailStillReconnectsWithACursor(t *testing.T) {
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
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"notifications":{},"_meta":{"com.dibs/serial":5}}}`+"\n\n")
		fl.Flush()
		// no mail, then the stream ends
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := inboxWatcher{reconnect: 50 * time.Millisecond}
	w.start(ctx, srv.Client(), srv.URL, "local-secret", "agent-token")
	<-bodies
	select {
	case second := <-bodies:
		params, _ := second["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if got, _ := meta["com.dibs/since"].(float64); got != 5 {
			t.Fatalf("the reconnect after an idle stream carries since=%v, want 5: a question that "+
				"arrived while the stream was down wakes nobody", meta["com.dibs/since"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher did not reconnect")
	}
}
